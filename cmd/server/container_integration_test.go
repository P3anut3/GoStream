package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt in only: two disposable containers on their own network. No host MySQL,
// production credentials, existing containers or user media are used.
func TestContainerAPIIntegration(t *testing.T) {
	if os.Getenv("GOSTREAM_DOCKER_TEST") != "1" {
		t.Skip("set GOSTREAM_DOCKER_TEST=1; requires built gostream-api:dev and mysql:8.4.9 images")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s failed: %v\n%s", args[0], err, output)
		}
		return strings.TrimSpace(string(output))
	}
	cleanup := func(args ...string) {
		t.Helper()
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if output, err := exec.CommandContext(cleanupCtx, "docker", args...).CombinedOutput(); err != nil {
			t.Errorf("test-owned Docker resource cleanup failed: %v\n%s", err, output)
		}
	}
	randomHex := func() string {
		t.Helper()
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(b[:])
	}
	apiImage := os.Getenv("GOSTREAM_DOCKER_IMAGE")
	if apiImage == "" {
		apiImage = "gostream-api:dev"
	}
	if docker("info", "--format", "{{.OSType}}") != "linux" {
		t.Fatal("requires Linux container engine")
	}
	for _, image := range []string{apiImage, "mysql:8.4.9"} {
		docker("image", "inspect", "--format", "{{.Id}}", image)
	}
	if got := docker("image", "inspect", "--format", "{{.Config.User}}|{{.Config.WorkingDir}}", apiImage); got != "10001:10001|/app" {
		t.Fatal("API image must use the expected non-root user and working directory")
	}
	name := "gostream-smoke-" + randomHex()[:12]
	dbName, apiName := name+"-mysql", name+"-api"
	docker("network", "create", "--label", "gostream.smoke="+name, name)
	t.Cleanup(func() { cleanup("network", "rm", name) })
	root := t.TempDir()
	writeEnvironment := func(filename, content string) string {
		t.Helper()
		path := filepath.Join(root, filename)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	password := randomHex()
	mysqlEnv := writeEnvironment("mysql.env", "MYSQL_ROOT_PASSWORD="+randomHex()+"\nMYSQL_DATABASE=gostream\nMYSQL_USER=gostream_smoke\nMYSQL_PASSWORD="+password+"\nTZ=Asia/Shanghai\n")
	schema, err := filepath.Abs("../../scripts/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	// The MySQL image's anonymous data volume belongs only to this fixture and
	// is removed with -v. No MySQL host port is published.
	docker("run", "--pull=never", "-d", "--name", dbName, "--label", "gostream.smoke="+name,
		"--network", name, "--network-alias", "mysql", "--env-file", mysqlEnv,
		"--mount", "type=bind,source="+schema+",target=/docker-entrypoint-initdb.d/001-schema.sql,readonly", "mysql:8.4.9")
	t.Cleanup(func() { cleanup("rm", "-f", "-v", dbName) })
	waitMySQL := func() {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			probeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			_, err := exec.CommandContext(probeCtx, "docker", "exec", dbName, "sh", "-c",
				`MYSQL_PWD="$MYSQL_PASSWORD" mysql --protocol=TCP --host=127.0.0.1 --user="$MYSQL_USER" "$MYSQL_DATABASE" --execute="SELECT 1 FROM users LIMIT 0"`).CombinedOutput()
			stop()
			if err == nil {
				return
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Fatal("isolated MySQL schema did not become ready")
	}
	t.Log("waiting for isolated MySQL schema")
	waitMySQL()
	apiEnv := writeEnvironment("api.env", fmt.Sprintf("GOSTREAM_DSN=gostream_smoke:%s@tcp(mysql:3306)/gostream?charset=utf8mb4&parseTime=true&loc=Local\nJWT_SECRET=%s\nGOSTREAM_FEED_CURSOR_SECRET=%s\nGOSTREAM_VIDEO_DIR=/app/test-media/videos\nGOSTREAM_COVER_DIR=/app/test-media/covers\n", password, randomHex(), randomHex()))
	docker("run", "--pull=never", "-d", "--name", apiName, "--label", "gostream.smoke="+name,
		"--network", name, "--env-file", apiEnv, "--publish", "127.0.0.1::8080", apiImage)
	t.Cleanup(func() { cleanup("rm", "-f", "-v", apiName) })
	address := docker("port", apiName, "8080/tcp")
	if !strings.HasPrefix(address, "127.0.0.1:") || strings.Contains(address, "\n") {
		t.Fatal("test API must publish one loopback-only port")
	}
	base := "http://" + address
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		response, err := client.Get(base + "/readyz")
		if err == nil {
			ready = response.StatusCode == 200
			response.Body.Close()
		}
		if ready {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		t.Fatal("test API did not become ready")
	}
	inspection := `test "$(id -u)" = 10001 && test -x /app/gostream && test -f /app/web/dist/index.html && test -f /app/internal/config/config.yaml && test -f /etc/ssl/certs/ca-certificates.crt && test "$(date +%z)" = +0800 && test ! -e /app/.env && test ! -e /app/internal/user/service.go && test ! -e /app/frontend/src && ! command -v go && ! command -v node`
	docker("exec", apiName, "sh", "-c", inspection)
	t.Log("image runtime verified: non-root, assets, YAML, timezone, no .env/source/compiler")
	request := func(method, path, token string, body io.Reader, contentType string, want int) []byte {
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
		response, err := client.Do(req)
		if err != nil {
			t.Fatalf("test request %s %s failed", method, path)
		}
		defer response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("%s %s expected %d, got %d", method, path, want, response.StatusCode)
		}
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	jsonRequest := func(path, token string, body any, want int) []byte {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return request("POST", path, token, bytes.NewReader(data), "application/json", want)
	}
	for _, path := range []string{"/", "/cover.svg", "/healthz", "/readyz"} {
		if len(request("GET", path, "", nil, "", 200)) == 0 {
			t.Fatalf("empty runtime asset %s", path)
		}
	}
	credentials := map[string]string{"username": "container_smoke", "password": randomHex()}
	jsonRequest("/users", "", credentials, 201)
	var login struct {
		ID    uint64 `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(jsonRequest("/login", "", credentials, 200), &login); err != nil || login.ID == 0 || login.Token == "" {
		t.Fatal("test account login failed")
	}
	upload := func(path, filename string, data []byte) (string, string) {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		response := request("POST", path, login.Token, &body, writer.FormDataContentType(), 200)
		var result struct {
			SavedName string `json:"saved_name"`
			PlayURL   string `json:"play_url"`
			CoverURL  string `json:"cover_url"`
		}
		if err := json.Unmarshal(response, &result); err != nil {
			t.Fatal("invalid upload metadata")
		}
		stem := strings.TrimSuffix(result.SavedName, filepath.Ext(result.SavedName))
		if decoded, err := hex.DecodeString(stem); err != nil || len(decoded) != 16 {
			t.Fatal("unexpected saved filename")
		}
		url, directory := result.PlayURL, "/app/test-media/videos/"
		if path == "/uploads/covers" {
			url, directory = result.CoverURL, "/app/test-media/covers/"
		}
		if !bytes.Equal(request("GET", url, "", nil, "", 200), data) {
			t.Fatal("served media differs from uploaded bytes")
		}
		if output := docker("exec", apiName, "cat", directory+result.SavedName); output != strings.TrimSpace(string(data)) {
			t.Fatal("upload not stored in the configured directory")
		}
		return url, result.SavedName
	}
	// MIME identification bytes, not a claim of playable video decoding.
	mp4 := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2', 0, 0, 0, 0, 'm', 'p', '4', '2', 'i', 's', 'o', 'm'}
	playURL, _ := upload("/uploads/videos", "demo.mp4", mp4)
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	coverURL, _ := upload("/uploads/covers", "cover.png", picture.Bytes())
	var published struct {
		ID uint64 `json:"id"`
	}
	if err := json.Unmarshal(jsonRequest("/videos", login.Token, map[string]string{"title": "Container smoke", "play_url": playURL, "cover_url": coverURL}, 201), &published); err != nil || published.ID == 0 {
		t.Fatal("container publication failed")
	}
	request("GET", fmt.Sprintf("/videos/%d", published.ID), "", nil, "", 200)
	rangeRequest, err := http.NewRequestWithContext(ctx, "GET", base+playURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	rangeRequest.Header.Set("Range", "bytes=0-7")
	rangeResponse, err := client.Do(rangeRequest)
	if err != nil {
		t.Fatal("container Range request failed")
	}
	rangeData, err := io.ReadAll(rangeResponse.Body)
	rangeResponse.Body.Close()
	if err != nil || rangeResponse.StatusCode != 206 || !bytes.Equal(rangeData, mp4[:8]) {
		t.Fatal("container byte-range response incorrect")
	}
	t.Log("container HTTP flow verified: register/login, video+cover upload to custom directories, publish, media and Range")
	docker("stop", "--timeout", "15", dbName)
	request("GET", "/readyz", "", nil, "", 503)
	request("GET", "/healthz", "", nil, "", 200)
	docker("start", dbName)
	waitMySQL()
	request("GET", "/readyz", "", nil, "", 200)
	request("GET", fmt.Sprintf("/videos/%d", published.ID), "", nil, "", 200)
	t.Log("isolated MySQL stop/restart verified: readiness 200→503→200, API remains alive, data retained")
	// No signal override: verify the image's default Docker stop behavior.
	docker("stop", "--timeout", "8", apiName)
	if docker("inspect", "--format", "{{.State.ExitCode}}", apiName) != "0" {
		t.Fatal("default SIGTERM shutdown should exit cleanly")
	}
	t.Log("default SIGTERM shutdown exit=0")
}
