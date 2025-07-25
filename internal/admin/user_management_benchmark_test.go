package admin

import (
	"testing"
)

// Note: GenerateSecretKey is already defined in user_management.go

// New implementation to compare

// Benchmark for current GenerateSecretKey
func BenchmarkGenerateSecretKey(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, err := GenerateSecretKey()
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark for new GenerateKey with length 40 (to match current output length)
func BenchmarkGenerateKeyWithBytes40(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, err := GenerateKeyWithBytes(40)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark for new GenerateKey with length 30 (to match current byte input)
func BenchmarkGenerateKeyWithBytes30(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, err := GenerateKeyWithBytes(30)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark for new GenerateKey with length 20 (common key length)
func BenchmarkGenerateKeyWithBytes20(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, err := GenerateKeyWithBytes(20)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Benchmark for GenerateKeyWithBuilder with different lengths
func BenchmarkGenerateKeyWithBuilder40(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, err := GenerateKeyWithBuilder(40)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGenerateKeyWithBuilder30(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, err := GenerateKeyWithBuilder(30)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGenerateKeyWithBuilder20(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, err := GenerateKeyWithBuilder(20)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Test to verify output characteristics
func TestGenerateKeyCharacteristics(t *testing.T) {
	// Test current implementation
	currentKey, err := GenerateSecretKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(currentKey) != 40 {
		t.Errorf("Current GenerateSecretKey returned length %d, expected 40", len(currentKey))
	}

	// Test new implementation with length 40
	newKey, err := GenerateKeyWithBytes(40)
	if err != nil {
		t.Fatal(err)
	}
	if len(newKey) != 40 {
		t.Errorf("New GenerateKey(40) returned length %d, expected 40", len(newKey))
	}

	// Verify new implementation only uses charset characters
	for _, char := range newKey {
		found := false
		for _, charsetChar := range charset {
			if char == charsetChar {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("New GenerateKey contains character '%c' not in charset", char)
		}
	}

	t.Logf("Current GenerateSecretKey: %s", currentKey)
	t.Logf("New GenerateKey(40): %s", newKey)

	// Test GenerateKeyWithBuilder implementation
	builderKey, err := GenerateKeyWithBuilder(40)
	if err != nil {
		t.Fatal(err)
	}
	if len(builderKey) != 40 {
		t.Errorf("GenerateKeyWithBuilder(40) returned length %d, expected 40", len(builderKey))
	}

	// Verify GenerateKeyWithBuilder only uses charset characters
	for _, char := range builderKey {
		found := false
		for _, charsetChar := range charset {
			if char == charsetChar {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("GenerateKeyWithBuilder contains character '%c' not in charset", char)
		}
	}

	t.Logf("GenerateKeyWithBuilder(40): %s", builderKey)
}
