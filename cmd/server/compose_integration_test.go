package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Uses a random project and credentials, ephemeral loopback ports, and project-
// owned volumes. It never reads .env or .env.compose and does not touch dev data.
func TestComposeEnvironmentIntegration(t *testing.T) {
	if os.Getenv("GOSTREAM_COMPOSE_TEST") != "1" {
		t.Skip("set GOSTREAM_COMPOSE_TEST=1; requires all Compose images locally")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	projectRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	randomHex := func() string {
		t.Helper()
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(b[:])
	}
	project := "gostream-compose-test-" + randomHex()[:12]
	mqPassword := randomHex()
	envPath := filepath.Join(t.TempDir(), "compose.env")
	content := "COMPOSE_API_PORT=0\nCOMPOSE_MYSQL_PORT=0\nCOMPOSE_REDIS_PORT=0\nCOMPOSE_AMQP_PORT=0\nCOMPOSE_RABBITMQ_UI_PORT=0\nCOMPOSE_RABBITMQ_USER=gostream\n"
	for key, value := range map[string]string{
		"COMPOSE_MYSQL_ROOT_PASSWORD": randomHex(), "COMPOSE_MYSQL_PASSWORD": randomHex(), "COMPOSE_REDIS_PASSWORD": randomHex(),
		"COMPOSE_RABBITMQ_PASSWORD": mqPassword, "JWT_SECRET": randomHex(), "GOSTREAM_FEED_CURSOR_SECRET": randomHex(),
	} {
		content += key + "=" + value + "\n"
	}
	if err := os.WriteFile(envPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	var childEnvironment []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if strings.HasPrefix(key, "COMPOSE_") || key == "JWT_SECRET" || key == "GOSTREAM_FEED_CURSOR_SECRET" {
			continue
		}
		childEnvironment = append(childEnvironment, entry)
	}
	baseArgs := []string{"compose", "--project-directory", projectRoot, "--env-file", envPath, "--project-name", project, "--file", filepath.Join(projectRoot, "compose.yaml")}
	run := func(compose bool, args ...string) string {
		t.Helper()
		if compose {
			args = append(append([]string{}, baseArgs...), args...)
		}
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Dir, cmd.Env = projectRoot, childEnvironment
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Docker test command failed: %v\n%s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	for _, image := range []string{"gostream-api:dev", "mysql:8.4.9", "redis:8-alpine", "rabbitmq:4.2-management"} {
		run(false, "image", "inspect", "--format", "{{.Id}}", image)
	}
	selector := "label=com.docker.compose.project=" + project
	if run(false, "ps", "-a", "--filter", selector, "--format", "{{.ID}}") != "" || run(false, "volume", "ls", "--filter", selector, "--format", "{{.Name}}") != "" {
		t.Fatal("random test project must not reuse existing resources")
	}
	run(true, "config", "--quiet")
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 90*time.Second)
		defer stop()
		args := append(append([]string{}, baseArgs...), "down", "--volumes", "--remove-orphans", "--timeout", "10")
		cmd := exec.CommandContext(cleanupCtx, "docker", args...)
		cmd.Dir, cmd.Env = projectRoot, childEnvironment
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("test project cleanup failed: %v\n%s", err, out)
		}
	})
	up := func() {
		t.Helper()
		run(true, "up", "--detach", "--wait", "--wait-timeout", "180", "--no-build", "--pull", "never")
	}
	endpoint := func(service, port string) string {
		t.Helper()
		address := run(true, "port", service, port)
		if !strings.HasPrefix(address, "127.0.0.1:") || strings.Contains(address, "\n") {
			t.Fatal("test port must use loopback only")
		}
		return "http://" + address
	}
	t.Log("starting isolated Compose project and waiting for four healthy services")
	up()
	for _, service := range []string{"api", "mysql", "redis", "rabbitmq"} {
		id := run(true, "ps", "-q", service)
		if run(false, "inspect", "--format", "{{.State.Health.Status}}", id) != "healthy" {
			t.Fatalf("%s is not healthy", service)
		}
	}
	client := &http.Client{Timeout: 5 * time.Second}
	apiBase, mqBase := endpoint("api", "8080"), endpoint("rabbitmq", "15672")
	if os.Getenv("GOSTREAM_COMPOSE_BROWSER_TEST") == "1" {
		// Browser tests target only this disposable project, never the dev stack.
		cmd := exec.CommandContext(ctx, "node", "node_modules/@playwright/test/cli.js", "test")
		cmd.Dir = filepath.Join(projectRoot, "frontend")
		cmd.Env = append(childEnvironment, "E2E_BASE_URL="+apiBase, "GOSTREAM_ISOLATED_BROWSER=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated Vue browser acceptance failed: %v\n%s", err, out)
		}
		t.Logf("isolated Vue browser acceptance passed:\n%s", out)
	}
	request := func(base, method, path, token string, body io.Reader, contentType string, want int, mq bool) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, base+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if mq {
			req.SetBasicAuth("gostream", mqPassword)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("test HTTP request %s %s failed", method, path)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s %s expected %d, got %d", method, path, want, resp.StatusCode)
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	jsonRequest := func(base, method, path, token string, body any, want int, mq bool) []byte {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return request(base, method, path, token, bytes.NewReader(data), "application/json", want, mq)
	}
	request(apiBase, "GET", "/healthz", "", nil, "", 200, false)
	request(apiBase, "GET", "/readyz", "", nil, "", 200, false)
	if !strings.Contains(string(request(apiBase, "GET", "/", "", nil, "", 200, false)), "GoStream") {
		t.Fatal("frontend asset missing")
	}
	credentials := map[string]string{"username": "compose_test", "password": randomHex()[:32]}
	jsonRequest(apiBase, "POST", "/users", "", credentials, 201, false)
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(jsonRequest(apiBase, "POST", "/login", "", credentials, 200, false), &login); err != nil || login.Token == "" {
		t.Fatal("Compose login failed")
	}
	mp4 := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2', 0, 0, 0, 0, 'm', 'p', '4', '2', 'i', 's', 'o', 'm'}
	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	part, err := writer.CreateFormFile("file", "compose.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(mp4); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var upload struct {
		PlayURL string `json:"play_url"`
	}
	if err := json.Unmarshal(request(apiBase, "POST", "/uploads/videos", login.Token, &multipartBody, writer.FormDataContentType(), 200, false), &upload); err != nil || !strings.HasPrefix(upload.PlayURL, "/media/videos/") {
		t.Fatal("Compose upload failed")
	}
	var published struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(jsonRequest(apiBase, "POST", "/videos", login.Token, map[string]string{"title": "Compose persistence", "play_url": upload.PlayURL, "cover_url": "/cover.svg"}, 201, false), &published); err != nil || published.ID == 0 {
		t.Fatal("Compose publish failed")
	}
	if !bytes.Equal(request(apiBase, "GET", upload.PlayURL, "", nil, "", 200, false), mp4) {
		t.Fatal("uploaded bytes changed")
	}
	redis := func(command string) string {
		t.Helper()
		return run(true, "exec", "-T", "redis", "sh", "-c", `REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli `+command)
	}
	if redis("PING") != "PONG" || redis("SET compose:fixture persistent") != "OK" {
		t.Fatal("Redis auth or write check failed")
	}
	if !strings.Contains(run(true, "exec", "-T", "redis", "redis-cli", "PING"), "NOAUTH") {
		t.Fatal("Redis must reject unauthenticated access")
	}
	jsonRequest(mqBase, "PUT", "/api/exchanges/%2F/compose-fixture", "", map[string]any{"type": "direct", "durable": true, "auto_delete": false, "internal": false, "arguments": map[string]any{}}, 201, true)
	jsonRequest(mqBase, "PUT", "/api/queues/%2F/compose-fixture", "", map[string]any{"durable": true, "auto_delete": false, "arguments": map[string]any{}}, 201, true)
	jsonRequest(mqBase, "POST", "/api/bindings/%2F/e/compose-fixture/q/compose-fixture", "", map[string]any{"routing_key": "fixture", "arguments": map[string]any{}}, 201, true)
	// Management HTTP is used only for this disposable infrastructure check;
	// future production consumers will use the AMQP client.
	var routed struct {
		Routed bool `json:"routed"`
	}
	if err := json.Unmarshal(jsonRequest(mqBase, "POST", "/api/exchanges/%2F/compose-fixture/publish", "", map[string]any{"properties": map[string]any{"delivery_mode": 2}, "routing_key": "fixture", "payload": "persistent-message", "payload_encoding": "string"}, 200, true), &routed); err != nil || !routed.Routed {
		t.Fatal("RabbitMQ test message was not routed")
	}
	t.Log("four services healthy; non-root upload, publish, Redis auth/write and durable RabbitMQ message verified")

	// The worker turns outbox events into notifications: another account likes the video, and
	// the author gets a like notification while the notifications queue drains.
	workerID := run(true, "ps", "-q", "worker")
	if run(false, "inspect", "--format", "{{.State.Status}}", workerID) != "running" {
		t.Fatal("the worker must be running")
	}
	var author, fan struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(request(apiBase, "GET", "/me", login.Token, nil, "", 200, false), &author); err != nil || author.ID == 0 {
		t.Fatal("Compose current user failed")
	}
	fanCredentials := map[string]string{"username": "compose_fan", "password": randomHex()[:32]}
	if err := json.Unmarshal(jsonRequest(apiBase, "POST", "/users", "", fanCredentials, 201, false), &fan); err != nil || fan.ID == 0 {
		t.Fatal("Compose second account failed")
	}
	var fanLogin struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(jsonRequest(apiBase, "POST", "/login", "", fanCredentials, 200, false), &fanLogin); err != nil || fanLogin.Token == "" {
		t.Fatal("Compose second login failed")
	}
	request(apiBase, "PUT", fmt.Sprintf("/videos/%d/like", published.ID), fanLogin.Token, nil, "", 204, false)
	// mysqlValue runs one query as the application user inside the MySQL container.
	mysqlValue := func(query string) string {
		t.Helper()
		return run(true, "exec", "-T", "mysql", "sh", "-c", `MYSQL_PWD="$MYSQL_PASSWORD" mysql -u"$MYSQL_USER" "$MYSQL_DATABASE" -N -e "`+query+`"`)
	}
	pendingEvents := func() string {
		t.Helper()
		return mysqlValue("SELECT COUNT(*) FROM outbox_events WHERE published_at IS NULL")
	}
	// notified counts the author's notifications of the given type about the fan and the video.
	notified := func(eventType string) string {
		t.Helper()
		return mysqlValue(fmt.Sprintf("SELECT COUNT(*) FROM notifications WHERE recipient_id = %d AND actor_id = %d AND video_id = %d AND event_type = '%s'",
			author.ID, fan.ID, published.ID, eventType))
	}
	// queued counts a queue's messages, unacknowledged ones included, without touching them.
	queued := func(queue string) string {
		t.Helper()
		for _, line := range strings.Split(run(true, "exec", "-T", "rabbitmq", "rabbitmqctl", "list_queues", "--quiet", "--no-table-headers", "name", "messages"), "\n") {
			if fields := strings.Fields(line); len(fields) == 2 && fields[0] == queue {
				return fields[1]
			}
		}
		t.Fatalf("queue %s not found", queue)
		return ""
	}
	waitFor := func(what string, done func() bool) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); !done(); time.Sleep(250 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting until %s", what)
			}
		}
	}
	// Every outbox event is sent and every queued message handled, the browser test's included,
	// and none of them ends up in the dead-letter queue.
	waitFor("the fan's like becomes a notification", func() bool { return notified("like.created") == "1" })
	waitFor("the outbox and the notifications queue are empty", func() bool {
		return pendingEvents() == "0" && queued("gostream.notifications") == "0"
	})
	if queued("gostream.notifications.dead") != "0" {
		t.Fatal("no event may end up in the dead-letter queue")
	}
	t.Log("worker turned the fan's like into a notification for the author")

	// While the worker is stopped, a new comment waits in the outbox.
	run(true, "stop", "--timeout", "8", "worker")
	if run(false, "inspect", "--format", "{{.State.ExitCode}}", workerID) != "0" {
		t.Fatal("the worker must exit cleanly on SIGTERM")
	}
	jsonRequest(apiBase, "POST", fmt.Sprintf("/videos/%d/comments", published.ID), fanLogin.Token, map[string]string{"content": "compose comment"}, 201, false)
	if pendingEvents() != "1" || notified("comment.created") != "0" {
		t.Fatal("while the worker is stopped the comment event must wait in the outbox")
	}

	apiID := run(true, "ps", "-q", "api")
	run(true, "stop", "--timeout", "8", "api")
	if run(false, "inspect", "--format", "{{.State.ExitCode}}", apiID) != "0" {
		t.Fatal("Compose default SIGTERM shutdown failed")
	}
	volumesBefore := strings.Fields(run(false, "volume", "ls", "--filter", selector, "--format", "{{.Name}}"))
	if len(volumesBefore) != 4 {
		t.Fatal("expected four project-owned named volumes")
	}
	sort.Strings(volumesBefore)
	run(true, "down", "--timeout", "10")
	volumesAfter := strings.Fields(run(false, "volume", "ls", "--filter", selector, "--format", "{{.Name}}"))
	sort.Strings(volumesAfter)
	if strings.Join(volumesBefore, "|") != strings.Join(volumesAfter, "|") {
		t.Fatal("down without -v must preserve named volumes")
	}
	t.Log("containers removed without -v; recreating against retained named volumes")
	up()
	apiBase, mqBase = endpoint("api", "8080"), endpoint("rabbitmq", "15672")
	if run(true, "ps", "-q", "api") == apiID {
		t.Fatal("persistence test must recreate the API container")
	}
	request(apiBase, "GET", "/me", login.Token, nil, "", 200, false)
	request(apiBase, "GET", fmt.Sprintf("/videos/%d", published.ID), "", nil, "", 200, false)
	if !bytes.Equal(request(apiBase, "GET", upload.PlayURL, "", nil, "", 200, false), mp4) {
		t.Fatal("media missing after container recreation")
	}
	if redis("GET compose:fixture") != "persistent" {
		t.Fatal("Redis data missing after container recreation")
	}
	var messages []struct {
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(jsonRequest(mqBase, "POST", "/api/queues/%2F/compose-fixture/get", "", map[string]any{"count": 1, "ackmode": "ack_requeue_false", "encoding": "auto", "truncate": 50000}, 200, true), &messages); err != nil || len(messages) != 1 || messages[0].Payload != "persistent-message" {
		t.Fatal("durable RabbitMQ message missing after container recreation")
	}
	// The recreated worker sends the waiting comment and turns it into a notification; the like
	// notification is still there, once.
	waitFor("the waiting comment becomes a notification", func() bool { return notified("comment.created") == "1" })
	if notified("like.created") != "1" || pendingEvents() != "0" || queued("gostream.notifications.dead") != "0" {
		t.Fatal("after recreation the like notification must be kept once and nothing may be left over")
	}
	t.Log("after down/up: account/JWT, video record, uploaded file, Redis value, RabbitMQ message and notification retained; the waiting comment became a notification")
}
