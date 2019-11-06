package relational

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/lestrrat-go/jwx/jwk"
	"github.com/offen/offen/server/keys"
	"github.com/offen/offen/server/persistence"
)

func (r *relationalDatabase) Login(email, password string) (persistence.LoginResult, error) {
	hashedEmail, hashedEmailErr := keys.HashEmail(email, r.emailSalt)
	if hashedEmailErr != nil {
		return persistence.LoginResult{}, hashedEmailErr
	}

	accountUser, err := r.findAccountUser(
		FindAccountUserQueryByHashedEmail(
			base64.StdEncoding.EncodeToString(hashedEmail),
		),
	)
	if err != nil {
		return persistence.LoginResult{}, fmt.Errorf("persistence: error looking up account user: %w", err)
	}

	saltBytes, saltErr := base64.StdEncoding.DecodeString(accountUser.Salt)
	if saltErr != nil {
		return persistence.LoginResult{}, fmt.Errorf("persistence: error decoding salt: %w", saltErr)
	}

	pwBytes, pwErr := base64.StdEncoding.DecodeString(accountUser.HashedPassword)
	if pwErr != nil {
		return persistence.LoginResult{}, fmt.Errorf("persistence: error decoding stored password: %w", pwErr)
	}
	if err := keys.ComparePassword(password, pwBytes); err != nil {
		return persistence.LoginResult{}, fmt.Errorf("persistence: error comparing passwords: %w", err)
	}

	pwDerivedKey, pwDerivedKeyErr := keys.DeriveKey(password, saltBytes)
	if pwDerivedKeyErr != nil {
		return persistence.LoginResult{}, fmt.Errorf("persistence: error deriving key from password: %w", pwDerivedKeyErr)
	}

	relationships, err := r.findAccountUserRelationships(
		FindAccountUserRelationShipsQueryByUserID(accountUser.UserID),
	)
	if err != nil {
		return persistence.LoginResult{}, fmt.Errorf("persistence: error retrieving account to user relationships: %w", err)
	}

	var results []persistence.LoginAccountResult
	for _, relationship := range relationships {
		chunks := strings.Split(relationship.PasswordEncryptedKeyEncryptionKey, " ")
		nonce, _ := base64.StdEncoding.DecodeString(chunks[0])
		key, _ := base64.StdEncoding.DecodeString(chunks[1])

		decryptedKey, decryptedKeyErr := keys.DecryptWith(pwDerivedKey, key, nonce)
		if decryptedKeyErr != nil {
			return persistence.LoginResult{}, fmt.Errorf("persistence: failed decrypting key encryption key for account %s: %v", relationship.AccountID, decryptedKeyErr)
		}
		k, kErr := jwk.New(decryptedKey)
		if kErr != nil {
			return persistence.LoginResult{}, kErr
		}

		account, err := r.findAccount(FindAccountQueryByID(relationship.AccountID))
		if err != nil {
			return persistence.LoginResult{}, fmt.Errorf(`persistence: error looking up account with id "%s": %w`, relationship.AccountID, err)
		}

		result := persistence.LoginAccountResult{
			AccountName:      account.Name,
			AccountID:        relationship.AccountID,
			KeyEncryptionKey: k,
		}
		results = append(results, result)
	}

	return persistence.LoginResult{
		UserID:   accountUser.UserID,
		Accounts: results,
	}, nil
}

func (r *relationalDatabase) LookupUser(userID string) (persistence.LoginResult, error) {
	accountUser, err := r.findAccountUser(
		FindAccountUserQueryByUserIDIncludeRelationships(userID),
	)
	if err != nil {
		return persistence.LoginResult{}, fmt.Errorf("persistence: error looking up account user: %w", err)
	}
	result := persistence.LoginResult{
		UserID:   accountUser.UserID,
		Accounts: []persistence.LoginAccountResult{},
	}
	for _, relationship := range accountUser.Relationships {
		result.Accounts = append(result.Accounts, persistence.LoginAccountResult{
			AccountID: relationship.AccountID,
		})
	}
	return result, nil
}

func (r *relationalDatabase) ChangePassword(userID, currentPassword, changedPassword string) error {
	accountUser, err := r.findAccountUser(
		FindAccountUserQueryByUserIDIncludeRelationships(userID),
	)
	if err != nil {
		return fmt.Errorf("persistence: error looking up account user: %w", err)
	}

	pwBytes, pwErr := base64.StdEncoding.DecodeString(accountUser.HashedPassword)
	if pwErr != nil {
		return fmt.Errorf("persistence: error decoding password: %v", pwErr)
	}
	if err := keys.ComparePassword(currentPassword, pwBytes); err != nil {
		return fmt.Errorf("persistence: current password did not match: %v", err)
	}

	saltBytes, saltErr := base64.StdEncoding.DecodeString(accountUser.Salt)
	if saltErr != nil {
		return fmt.Errorf("persistence: error decoding salt: %v", saltErr)
	}

	keyFromCurrentPassword, keyErr := keys.DeriveKey(currentPassword, saltBytes)
	if keyErr != nil {
		return keyErr
	}

	keyFromChangedPassword, keyErr := keys.DeriveKey(changedPassword, saltBytes)
	if keyErr != nil {
		return keyErr
	}

	newPasswordHash, hashErr := keys.HashPassword(changedPassword)
	if hashErr != nil {
		return fmt.Errorf("persistence: error hashing new password: %v", hashErr)
	}

	accountUser.HashedPassword = base64.StdEncoding.EncodeToString(newPasswordHash)
	// TODO: run the following in a transaction
	if err := r.updateAccountUser(&accountUser); err != nil {
		return err
	}

	for _, relationship := range accountUser.Relationships {
		chunks := strings.Split(relationship.PasswordEncryptedKeyEncryptionKey, " ")
		nonce, _ := base64.StdEncoding.DecodeString(chunks[0])
		value, _ := base64.StdEncoding.DecodeString(chunks[1])
		decryptedKey, decryptionErr := keys.DecryptWith(keyFromCurrentPassword, value, nonce)
		if decryptionErr != nil {
			return decryptionErr
		}
		reencryptedKey, nonce, reencryptionErr := keys.EncryptWith(keyFromChangedPassword, decryptedKey)
		if reencryptionErr != nil {
			return reencryptionErr
		}
		relationship.PasswordEncryptedKeyEncryptionKey = base64.StdEncoding.EncodeToString(nonce) + " " + base64.StdEncoding.EncodeToString(reencryptedKey)
		if err := r.updateAccountUserRelationship(&relationship); err != nil {
			return fmt.Errorf("persistence: error updating keys on relationship: %w", err)
		}
	}
	return nil
}

func (r *relationalDatabase) ResetPassword(emailAddress, password string, oneTimeKey []byte) error {
	hashedEmail, hashErr := keys.HashEmail(emailAddress, r.emailSalt)
	if hashErr != nil {
		return fmt.Errorf("error hashing given email address: %w", hashErr)
	}

	accountUser, err := r.findAccountUser(
		FindAccountUserQueryByHashedEmail(
			base64.StdEncoding.EncodeToString(hashedEmail),
		),
	)
	if err != nil {
		return fmt.Errorf("persistence: error looking up account user: %w", err)
	}

	saltBytes, saltErr := base64.StdEncoding.DecodeString(accountUser.Salt)
	if saltErr != nil {
		return fmt.Errorf("persistence: error decoding salt for account user: %w", saltErr)
	}

	passwordDerivedKey, deriveErr := keys.DeriveKey(password, saltBytes)
	if deriveErr != nil {
		return fmt.Errorf("persistence: error deriving key from password: %w", deriveErr)
	}

	relationships, err := r.findAccountUserRelationships(
		FindAccountUserRelationShipsQueryByUserID(accountUser.UserID),
	)
	if err != nil {
		return fmt.Errorf("persistence: error looking up relationships: %w", err)
	}

	// TODO: run the following code in a transaction
	for _, relationship := range relationships {
		chunks := strings.Split(relationship.OneTimeEncryptedKeyEncryptionKey, " ")
		nonce, _ := base64.StdEncoding.DecodeString(chunks[0])
		cipher, _ := base64.StdEncoding.DecodeString(chunks[1])
		keyEncryptionKey, decryptionErr := keys.DecryptWith(oneTimeKey, cipher, nonce)
		if decryptionErr != nil {
			return fmt.Errorf("persistence: error decrypting key encryption key: %v", decryptionErr)
		}
		passwordEncryptedKey, passwordNonce, encryptionErr := keys.EncryptWith(passwordDerivedKey, keyEncryptionKey)
		if encryptionErr != nil {
			return fmt.Errorf("persistence: error re-encrypting key encryption key: %v", encryptionErr)
		}
		relationship.PasswordEncryptedKeyEncryptionKey = fmt.Sprintf(
			"%s %s",
			base64.StdEncoding.EncodeToString(passwordNonce),
			base64.StdEncoding.EncodeToString(passwordEncryptedKey),
		)
		relationship.OneTimeEncryptedKeyEncryptionKey = ""
		if err := r.updateAccountUserRelationship(&relationship); err != nil {
			return fmt.Errorf("persistence: error updating keys on relationship: %w", err)
		}
	}
	passwordHash, hashErr := keys.HashPassword(password)
	if hashErr != nil {
		return fmt.Errorf("persistence: error hashing password: %v", hashErr)
	}
	accountUser.HashedPassword = base64.StdEncoding.EncodeToString(passwordHash)
	if err := r.updateAccountUser(&accountUser); err != nil {
		return fmt.Errorf("persistence: error updating password on account user: %w", err)
	}
	return nil
}

func (r *relationalDatabase) ChangeEmail(userID, emailAddress, password string) error {
	accountUser, err := r.findAccountUser(
		FindAccountUserQueryByUserIDIncludeRelationships(userID),
	)
	if err != nil {
		return fmt.Errorf("persistence: error looking up account user: %w", err)
	}

	pwBytes, pwErr := base64.StdEncoding.DecodeString(accountUser.HashedPassword)
	if pwErr != nil {
		return fmt.Errorf("persistence: error decoding password: %v", pwErr)
	}
	if err := keys.ComparePassword(password, pwBytes); err != nil {
		return fmt.Errorf("persistence: current password did not match: %v", err)
	}

	saltBytes, saltErr := base64.StdEncoding.DecodeString(accountUser.Salt)
	if saltErr != nil {
		return fmt.Errorf("persistence: error decoding salt: %v", saltErr)
	}

	keyFromCurrentPassword, keyErr := keys.DeriveKey(password, saltBytes)
	if keyErr != nil {
		return fmt.Errorf("persistence: error deriving key from password: %v", keyErr)
	}

	emailDerivedKey, deriveKeyErr := keys.DeriveKey(emailAddress, saltBytes)
	if deriveKeyErr != nil {
		return fmt.Errorf("persistence: error deriving key from email address: %v", deriveKeyErr)
	}

	hashedEmail, hashErr := keys.HashEmail(emailAddress, r.emailSalt)
	if hashErr != nil {
		return fmt.Errorf("persistence: error hashing updated email address: %v", hashErr)
	}

	// TODO: run the following code in a transaction
	accountUser.HashedEmail = base64.StdEncoding.EncodeToString(hashedEmail)
	if err := r.updateAccountUser(&accountUser); err != nil {
		return fmt.Errorf("persistence: error updating hashed email on account user: %w", err)
	}
	for _, relationship := range accountUser.Relationships {
		chunks := strings.Split(relationship.PasswordEncryptedKeyEncryptionKey, " ")
		nonce, _ := base64.StdEncoding.DecodeString(chunks[0])
		value, _ := base64.StdEncoding.DecodeString(chunks[1])
		decryptedKey, decryptionErr := keys.DecryptWith(keyFromCurrentPassword, value, nonce)
		if decryptionErr != nil {
			return decryptionErr
		}
		reencryptedKey, nonce, reencryptionErr := keys.EncryptWith(emailDerivedKey, decryptedKey)
		if reencryptionErr != nil {
			return reencryptionErr
		}
		relationship.EmailEncryptedKeyEncryptionKey = base64.StdEncoding.EncodeToString(nonce) + " " + base64.StdEncoding.EncodeToString(reencryptedKey)
		if err := r.updateAccountUserRelationship(&relationship); err != nil {
			return fmt.Errorf("persistence: error updating keys on relationship: %w", err)
		}
	}
	return nil
}

func (r *relationalDatabase) GenerateOneTimeKey(emailAddress string) ([]byte, error) {
	hashedEmail, hashErr := keys.HashEmail(emailAddress, r.emailSalt)
	if hashErr != nil {
		return nil, fmt.Errorf("error hashing given email address: %v", hashErr)
	}

	accountUser, err := r.findAccountUser(
		FindAccountUserQueryByHashedEmailIncludeRelationships(
			base64.StdEncoding.EncodeToString(hashedEmail),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("persistence: error looking up account user: %w", err)
	}

	saltBytes, saltErr := base64.StdEncoding.DecodeString(accountUser.Salt)
	if saltErr != nil {
		return nil, fmt.Errorf("error decoding salt for account user: %v", saltErr)
	}

	emailDerivedKey, deriveErr := keys.DeriveKey(emailAddress, saltBytes)
	if deriveErr != nil {
		return nil, fmt.Errorf("error deriving key from email address: %v", deriveErr)
	}
	oneTimeKey, _ := keys.GenerateRandomValue(keys.DefaultEncryptionKeySize)
	oneTimeKeyBytes, _ := base64.StdEncoding.DecodeString(oneTimeKey)

	// TODO: run the following in a transaction
	for _, relationship := range accountUser.Relationships {
		chunks := strings.Split(relationship.EmailEncryptedKeyEncryptionKey, " ")
		nonce, _ := base64.StdEncoding.DecodeString(chunks[0])
		cipher, _ := base64.StdEncoding.DecodeString(chunks[1])
		decryptedKey, decryptErr := keys.DecryptWith(emailDerivedKey, cipher, nonce)
		if decryptErr != nil {
			return nil, fmt.Errorf("persistence: error decrypting email encrypted key: %v", decryptErr)
		}
		oneTimeEncryptedKey, nonce, encryptErr := keys.EncryptWith(oneTimeKeyBytes, decryptedKey)
		if encryptErr != nil {
			return nil, fmt.Errorf("persistence: error encrypting key with one time key: %v", encryptErr)
		}
		relationship.OneTimeEncryptedKeyEncryptionKey = fmt.Sprintf(
			"%s %s",
			base64.StdEncoding.EncodeToString(nonce),
			base64.StdEncoding.EncodeToString(oneTimeEncryptedKey),
		)
		if err := r.updateAccountUserRelationship(&relationship); err != nil {
			return nil, fmt.Errorf("persistence: error updating relationship record: %v", err)
		}
	}
	return oneTimeKeyBytes, nil
}
