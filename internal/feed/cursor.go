package feed

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

/*
把“上次读到哪里”变成一个可以交给前端保存、下次再带回来的字符串。
*/

// CursorCodec 只编码续读位置，不签发登录凭证，也不授予访问权限。
type CursorCodec struct {
	secret []byte
}

// cursorPayload 是内部格式。前端只需原样回传编码后的字符串。
type cursorPayload struct {
	Version   int       `json:"v"`
	Kind      string    `json:"kind"`       //标记属于哪种流，当前为 "latest"，比如未来增加推荐流，就不能把它的游标当作最新流的位置使用。Kind 可以帮助检查这种错误。
	CreatedAt time.Time `json:"created_at"` //真正的查询位置
	ID        uint64    `json:"id"`
}

func NewCursorCodec(secret []byte) (*CursorCodec, error) { // 编码和解码工具
	if len(secret) < 32 {
		return nil, errors.New("Feed 游标签名密钥至少需要 32 字节")
	}
	return &CursorCodec{secret: append([]byte(nil), secret...)}, nil //复制出一个新的字节切片
}

func (c *CursorCodec) Encode(position Position) (string, error) { // 把位置变成字符串
	if position.CreatedAt.IsZero() || position.ID == 0 { //检查位置是否完整
		return "", ErrInvalidCursor
	}
	payload, err := json.Marshal(cursorPayload{ // 组装并且转化为JSON
		Version: 1, Kind: "latest", CreatedAt: position.CreatedAt.UTC(), ID: position.ID,
	})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload) // 用 Base64URL 编码
	signature := base64.RawURLEncoding.EncodeToString(c.sign(encoded))
	return encoded + "." + signature, nil
}

// Decode 将空字符串解释为第一批；其他值必须通过格式、签名和载荷检查。
func (c *CursorCodec) Decode(token string) (*Position, error) {
	if token == "" {
		return nil, nil
	}
	if len(token) > 1024 {
		return nil, ErrInvalidCursor
	}
	encoded, signatureText, found := strings.Cut(token, ".")
	if !found || encoded == "" || signatureText == "" {
		return nil, ErrInvalidCursor
	}
	signature, err := base64.RawURLEncoding.Strict().DecodeString(signatureText)
	if err != nil || !hmac.Equal(signature, c.sign(encoded)) {
		return nil, ErrInvalidCursor
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	var payload cursorPayload
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, ErrInvalidCursor
	}
	// 不能接受有效载荷后又拼接另一个 JSON 值。
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, ErrInvalidCursor
	}
	if payload.Version != 1 || payload.Kind != "latest" || payload.CreatedAt.IsZero() || payload.ID == 0 {
		return nil, ErrInvalidCursor
	}
	return &Position{CreatedAt: payload.CreatedAt.UTC(), ID: payload.ID}, nil
}

func (c *CursorCodec) sign(encoded string) []byte {
	mac := hmac.New(sha256.New, c.secret)
	// 固定用途前缀进一步区分此签名与其他协议中的签名。
	mac.Write([]byte("gostream:feed:cursor:"))
	mac.Write([]byte(encoded))
	return mac.Sum(nil)
}
