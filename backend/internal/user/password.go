package user

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// 前缀标识本应用的固定参数版本，后续修改参数时可以继续验证已有哈希。
const (
	passwordHashPrefix = "$im-argon2id-v1$"
	passwordSaltSize   = 16
	passwordKeySize    = 32
)

func derivePasswordKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, passwordKeySize)
}

func hashPassword(password string) (string, error) {
	var salt [passwordSaltSize]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	key := derivePasswordKey(password, salt[:])
	return passwordHashPrefix + base64.RawStdEncoding.EncodeToString(append(salt[:], key...)), nil
}

func matchesPassword(stored, password string) bool {
	encoded, isArgon2 := strings.CutPrefix(stored, passwordHashPrefix)
	if !isArgon2 {
		// 旧账号的输入范围维持原来的 bcrypt 上限。
		return len(password) <= 72 && bcrypt.CompareHashAndPassword([]byte(stored), []byte(password)) == nil
	}
	payload, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(payload) != passwordSaltSize+passwordKeySize {
		return false
	}
	key := derivePasswordKey(password, payload[:passwordSaltSize])
	return subtle.ConstantTimeCompare(key, payload[passwordSaltSize:]) == 1
}
