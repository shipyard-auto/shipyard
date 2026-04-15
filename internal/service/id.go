package service

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

const idAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

type IDGenerator interface {
	NewID(existing map[string]struct{}) (string, error)
}

type RandomIDGenerator struct{}

func (RandomIDGenerator) NewID(existing map[string]struct{}) (string, error) {
	for range 64 {
		id, err := randomID(6)
		if err != nil {
			return "", err
		}
		if _, found := existing[id]; !found {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not generate a unique service id")
}

func randomID(length int) (string, error) {
	bytes := make([]byte, length)
	for i := range bytes {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(idAlphabet))))
		if err != nil {
			return "", fmt.Errorf("generate random service id: %w", err)
		}
		bytes[i] = idAlphabet[n.Int64()]
	}
	return string(bytes), nil
}

