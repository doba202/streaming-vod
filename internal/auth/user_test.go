package auth

import (
	"errors"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestMain(m *testing.M) {
	hashCost = bcrypt.MinCost // test nhanh; production dùng 12
	os.Exit(m.Run())
}

func TestHashAndCheckPassword(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$2a$") {
		t.Errorf("hash = %q, want bcrypt", h)
	}
	if !CheckPassword(h, "correct horse") {
		t.Error("đúng mật khẩu phải khớp")
	}
	if CheckPassword(h, "wrong horse") || CheckPassword("not-a-hash", "correct horse") {
		t.Error("sai mật khẩu hoặc hash hỏng phải không khớp")
	}
}

func TestNormalizeEmail(t *testing.T) {
	got, err := NormalizeEmail("  An.Nguyen@Example.COM ")
	if err != nil || got != "an.nguyen@example.com" {
		t.Errorf("got %q, %v", got, err)
	}
	for _, bad := range []string{"", "abc", "a@", "An <a@b.com>", strings.Repeat("a", 250) + "@b.com"} {
		if _, err := NormalizeEmail(bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("12345678"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"1234567", strings.Repeat("x", 73)} {
		if err := ValidatePassword(bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("len %d: err = %v", len(bad), err)
		}
	}
}
