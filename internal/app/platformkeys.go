package app

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"

	"git-ctx/internal/apikey"
	"git-ctx/internal/backup"
	secretstore "git-ctx/internal/secret"
	"git-ctx/internal/store"
)

// Where this platform's own encryption keys come from.
//
// They were derived from the database DSN, which meant they were stored
// nowhere and recomputed from the connection string at every start. That works
// until the connection string changes, and an on-premises installation changes
// it for reasons that have nothing to do with security: the data directory
// moves, a relative path becomes absolute, two query parameters swap places, a
// PostgreSQL host is renamed. The platform then could not decrypt its own
// settings and refused to start, reporting "cipher: message authentication
// failed" — which names the primitive and not the cause — and every API key
// stopped authenticating, silently, because the pepper had changed with it.
//
// The keys are now written down, wrapped by a key derived from the recovery
// key. On an installation that already has data, the values captured are the
// ones the DSN produced, so nothing has to be re-encrypted and no API key
// stops working: the same secrets simply stop depending on a string that is
// allowed to change.

const platformKeyRow = "primary"

// wrappingKey derives the key that protects the stored keys. The recovery key
// is required by config.FromEnv, is meant to be stable across restarts and
// moves, and is already the one secret an operator is told to keep.
func wrappingKey(recoveryKey string) (cipher.AEAD, error) {
	sum := sha256.Sum256([]byte("git-ctx/key-wrap/v1\x00" + recoveryKey))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func wrap(aead cipher.AEAD, secret string) ([]byte, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, aead.Seal(nil, nonce, []byte(secret), []byte("git-ctx/platform-keys/v1"))...), nil
}

func unwrap(aead cipher.AEAD, sealed []byte) (string, error) {
	if len(sealed) < aead.NonceSize() {
		return "", errors.New("wrapped key is truncated")
	}
	raw, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte("git-ctx/platform-keys/v1"))
	return string(raw), err
}

// resolveKeys returns the master key and API-key pepper this process must use.
//
// The stored pair wins when it can be unwrapped. Otherwise the pair the
// configuration derived is checked against whatever is already sealed in the
// database, and if it opens it — or if nothing is sealed yet — it is written
// down so that this is the last start that depends on the DSN.
func resolveKeys(ctx context.Context, s *store.Store, recoveryKey, derivedMaster, derivedPepper string) (string, string, error) {
	wrapper, err := wrappingKey(recoveryKey)
	if err != nil {
		return "", "", err
	}
	var masterSealed, pepperSealed []byte
	err = s.DB.QueryRowContext(ctx, s.Rebind(
		`SELECT master_key_wrapped,api_key_pepper_wrapped FROM platform_keys WHERE id=?`), platformKeyRow).
		Scan(&masterSealed, &pepperSealed)
	switch {
	case err == nil:
		master, masterErr := unwrap(wrapper, masterSealed)
		pepper, pepperErr := unwrap(wrapper, pepperSealed)
		if masterErr == nil && pepperErr == nil {
			return master, pepper, nil
		}
		// The recovery key changed. The derived pair may still be the right one,
		// in which case the stored pair is rewritten under the new wrapping key.
		if sealedDataOpensWith(ctx, s, derivedMaster) {
			return derivedMaster, derivedPepper, storeKeys(ctx, s, wrapper, derivedMaster, derivedPepper)
		}
		return "", "", fmt.Errorf("this platform cannot open its own keys: they are stored wrapped by GIT_CTX_RECOVERY_KEY and this process's recovery key does not open them, and the fallback derived from the database DSN does not open the stored settings either. Start with the recovery key this installation was created with")
	case errors.Is(err, sql.ErrNoRows):
		if !sealedDataOpensWith(ctx, s, derivedMaster) {
			return "", "", fmt.Errorf("this platform cannot decrypt its own settings. Until now the encryption key was derived from the database connection string, so changing that string — moving the data directory, making a relative path absolute, reordering query parameters — changes the key. Start once with the original GIT_CTX_DB_DSN, which writes the keys into the database, after which the connection string may change freely")
		}
		return derivedMaster, derivedPepper, storeKeys(ctx, s, wrapper, derivedMaster, derivedPepper)
	default:
		return "", "", err
	}
}

func storeKeys(ctx context.Context, s *store.Store, wrapper cipher.AEAD, master, pepper string) error {
	masterSealed, err := wrap(wrapper, master)
	if err != nil {
		return err
	}
	pepperSealed, err := wrap(wrapper, pepper)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, s.Rebind(
		`INSERT INTO platform_keys(id,master_key_wrapped,api_key_pepper_wrapped) VALUES(?,?,?)
		 ON CONFLICT(id) DO UPDATE SET master_key_wrapped=excluded.master_key_wrapped,api_key_pepper_wrapped=excluded.api_key_pepper_wrapped`),
		platformKeyRow, masterSealed, pepperSealed)
	return err
}

// sealedDataOpensWith reports whether a candidate master key opens what is
// already sealed. A database with nothing sealed yet answers true: any key is
// the right key for an installation that has not written a secret.
func sealedDataOpensWith(ctx context.Context, s *store.Store, master string) bool {
	block, err := aes.NewCipher([]byte(master))
	if err != nil {
		return false
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return false
	}
	var sealed []byte
	err = s.DB.QueryRowContext(ctx, `SELECT token_encrypted FROM platform_bootstrap WHERE id='initial-admin'`).Scan(&sealed)
	if err == nil {
		if len(sealed) < aead.NonceSize() {
			return false
		}
		_, openErr := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte("git-ctx/bootstrap/v1"))
		return openErr == nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false
	}
	var settings []byte
	err = s.DB.QueryRowContext(ctx, `SELECT value_encrypted FROM system_settings LIMIT 1`).Scan(&settings)
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	if err != nil {
		return false
	}
	if len(settings) < aead.NonceSize() {
		return false
	}
	_, openErr := aead.Open(nil, settings[:aead.NonceSize()], settings[aead.NonceSize():], nil)
	return openErr == nil
}

// backupWrappingKey derives the key backups are sealed with.
//
// It has a label of its own so that a backup and the stored key material are
// not protected by the same derived secret, while both still follow from the
// one thing the operator keeps.
func backupWrappingKey(recoveryKey string) (cipher.AEAD, error) {
	sum := sha256.Sum256([]byte("git-ctx/backup-seal/v1\x00" + recoveryKey))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// reloadKeys picks up key material that arrived under a running process.
//
// A restore replaces the whole database, key material included, while this
// process holds the keys it resolved when it started. Without this the restore
// succeeds and the settings it just brought back cannot be decrypted — which is
// how a recovery ends with "Unable to decrypt setting" on a database that is
// perfectly intact. The caller holds the request gate and has stopped
// background work, so this is the moment nothing else is using them.
func (a *App) reloadKeys(ctx context.Context) error {
	master, pepper, err := resolveKeys(ctx, a.store, a.cfg.RecoveryKey, a.cfg.MasterKey, a.cfg.KeyPepper)
	if err != nil {
		return err
	}
	if master == a.cfg.MasterKey && pepper == a.cfg.KeyPepper {
		return nil
	}
	block, err := aes.NewCipher([]byte(master))
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	backupKey, err := backupWrappingKey(a.cfg.RecoveryKey)
	if err != nil {
		return err
	}
	a.cfg.MasterKey, a.cfg.KeyPepper = master, pepper
	a.aead = aead
	a.keys = apikey.New(a.store, pepper)
	a.keys.SetRateLimitAlertLoader(a.rateLimitAlertsEnabled)
	a.backup = backup.New(a.store, aead, backupKey, a.backupConfig)
	a.secrets = secretstore.New(a.store, a.seal, a.open, a.vaultClient)
	return nil
}
