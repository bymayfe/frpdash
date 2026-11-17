package auth

import "golang.org/x/crypto/bcrypt"

// PasswordHasher hashes and verifies passwords.
type PasswordHasher interface {
	Hash(password string) (string, error)
	// Compare returns nil if password matches hash.
	Compare(hash, password string) error
}

// DefaultBcryptCost is the production bcrypt work factor.
const DefaultBcryptCost = 12

// BcryptHasher is a PasswordHasher backed by bcrypt.
type BcryptHasher struct{ Cost int }

// Hash implements PasswordHasher.
func (h BcryptHasher) Hash(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), h.Cost)
	return string(b), err
}

// Compare implements PasswordHasher.
func (h BcryptHasher) Compare(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}
