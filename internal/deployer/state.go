package deployer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// MetadataFile is the name of the deployment metadata file
const MetadataFile = "metadata.json"

// DeploymentMetadata contains persisted deployment information
type DeploymentMetadata struct {
	ID            string           `json:"id"`
	App           string           `json:"app"`
	Branch        string           `json:"branch"`
	SanitizedName string           `json:"sanitized_name"`
	URL           string           `json:"url"`
	ImageTag      string           `json:"image_tag"`
	CreatedAt     string           `json:"created_at"`
	UpdatedAt     string           `json:"updated_at"`
	Status        DeploymentStatus `json:"status"`
	ErrorMessage  string           `json:"error_message,omitempty"`
	// Generic encrypted passwords (JSON map encrypted as single blob)
	PasswordsEncrypted string `json:"passwords_encrypted,omitempty"`
	// Deprecated: kept for backward compat migration
	DBPasswordEncrypted string `json:"db_password_encrypted,omitempty"`
}

// StateManager handles deployment state persistence
type StateManager struct {
	dataDir       string
	encryptionKey []byte // 32 bytes for AES-256
}

// NewStateManager creates a new state manager
// If encryptionKey is empty, secrets will not be encrypted (not recommended for production)
func NewStateManager(dataDir string, encryptionKey []byte) *StateManager {
	return &StateManager{
		dataDir:       dataDir,
		encryptionKey: encryptionKey,
	}
}

// SaveDeployment persists deployment metadata to disk.
// When passwords is nil, existing encrypted passwords are preserved from disk.
func (sm *StateManager) SaveDeployment(dep *Deployment, passwords map[string]string) error {
	deployDir := filepath.Join(sm.dataDir, "deployments", dep.App, dep.SanitizedName)

	// Ensure directory exists
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		return fmt.Errorf("creating deployment directory: %w", err)
	}

	metadata := DeploymentMetadata{
		ID:            dep.ID,
		App:           dep.App,
		Branch:        dep.Branch,
		SanitizedName: dep.SanitizedName,
		URL:           dep.URL,
		ImageTag:      dep.ImageTag,
		CreatedAt:     dep.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:     dep.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		Status:        dep.Status,
		ErrorMessage:  dep.ErrorMessage,
	}

	if len(passwords) > 0 && len(sm.encryptionKey) == 32 {
		// Serialize and encrypt the passwords map
		pwJSON, err := json.Marshal(passwords)
		if err != nil {
			return fmt.Errorf("marshaling passwords: %w", err)
		}
		encrypted, err := sm.encrypt(string(pwJSON))
		if err != nil {
			return fmt.Errorf("encrypting passwords: %w", err)
		}
		metadata.PasswordsEncrypted = encrypted
	} else if passwords == nil {
		// Preserve existing encrypted passwords from disk
		existing, err := sm.LoadDeployment(dep.App, dep.SanitizedName)
		if err == nil && existing != nil {
			metadata.PasswordsEncrypted = existing.PasswordsEncrypted
		}
	}

	metadataPath := filepath.Join(deployDir, MetadataFile)
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling metadata: %w", err)
	}

	// Write with restricted permissions (owner read/write only)
	if err := os.WriteFile(metadataPath, data, 0600); err != nil {
		return fmt.Errorf("writing metadata file: %w", err)
	}

	return nil
}

// LoadDeployment loads deployment metadata from disk
func (sm *StateManager) LoadDeployment(app, sanitizedBranch string) (*DeploymentMetadata, error) {
	metadataPath := filepath.Join(sm.dataDir, "deployments", app, sanitizedBranch, MetadataFile)

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading metadata file: %w", err)
	}

	var metadata DeploymentMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("parsing metadata: %w", err)
	}

	return &metadata, nil
}

// LoadPasswords decrypts and returns the passwords map for a deployment.
// If the deployment has a legacy DBPasswordEncrypted field, it is migrated.
func (sm *StateManager) LoadPasswords(app, sanitizedBranch string) (map[string]string, error) {
	metadata, err := sm.LoadDeployment(app, sanitizedBranch)
	if err != nil {
		return nil, err
	}
	if metadata == nil {
		return nil, nil
	}

	// New-style generic passwords
	if metadata.PasswordsEncrypted != "" {
		if len(sm.encryptionKey) != 32 {
			return nil, fmt.Errorf("encryption key not configured")
		}
		plaintext, err := sm.decrypt(metadata.PasswordsEncrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypting passwords: %w", err)
		}
		var passwords map[string]string
		if err := json.Unmarshal([]byte(plaintext), &passwords); err != nil {
			return nil, fmt.Errorf("parsing passwords: %w", err)
		}
		return passwords, nil
	}

	// Backward compat: migrate old single DB password
	if metadata.DBPasswordEncrypted != "" {
		if len(sm.encryptionKey) != 32 {
			return nil, fmt.Errorf("encryption key not configured")
		}
		pw, err := sm.decrypt(metadata.DBPasswordEncrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypting legacy db password: %w", err)
		}
		return map[string]string{"dbpass": pw}, nil
	}

	return nil, nil
}

// encrypt encrypts a plaintext string using AES-256-GCM
func (sm *StateManager) encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(sm.encryptionKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// decrypt decrypts a ciphertext string using AES-256-GCM
func (sm *StateManager) decrypt(ciphertext string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(sm.encryptionKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	if len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertextBytes := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertextBytes, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

// LoadAllDeployments loads all deployment metadata from the data directory
func (sm *StateManager) LoadAllDeployments() ([]*DeploymentMetadata, error) {
	deploymentsDir := filepath.Join(sm.dataDir, "deployments")

	apps, err := os.ReadDir(deploymentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading deployments directory: %w", err)
	}

	var deployments []*DeploymentMetadata

	for _, app := range apps {
		if !app.IsDir() {
			continue
		}

		branches, err := os.ReadDir(filepath.Join(deploymentsDir, app.Name()))
		if err != nil {
			continue
		}

		for _, branch := range branches {
			if !branch.IsDir() {
				continue
			}

			metadata, err := sm.LoadDeployment(app.Name(), branch.Name())
			if err != nil {
				continue
			}
			if metadata != nil {
				deployments = append(deployments, metadata)
			}
		}
	}

	return deployments, nil
}
