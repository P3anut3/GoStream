package user

import "testing"

func TestPassword(t *testing.T) {
	hash, err := HashPassword("abc12345")
	if err != nil {
		t.Fatalf("生成密码哈希失败：%v", err)
	}

	t.Run("正确密码通过", func(t *testing.T) {
		if err := CheckPassword(hash, "abc12345"); err != nil {
			t.Errorf("正确密码校验失败：%v", err)
		}
	})

	t.Run("错误密码被拒绝", func(t *testing.T) {
		if err := CheckPassword(hash, "wrong-password"); err == nil {
			t.Error("错误密码不应该校验通过")
		}
	})
}
