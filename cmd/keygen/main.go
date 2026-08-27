package main

import (
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run ./cmd/keygen <tenant-id>")
		os.Exit(1)
	}

	tenantID := os.Args[1]
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		secret = "super-secret-local-dev-key"
	}

	claims := jwt.MapClaims{
		"sub": tenantID,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(30 * 24 * time.Hour).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		fmt.Printf("Error signing token: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Generated JWT for %q (expires in 30 days):\n%s\n", tenantID, signed)
}
