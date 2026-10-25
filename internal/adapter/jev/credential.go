package jev

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/y3owk1n/neru/internal/derrors"
)

const (
	// keyHomePath is the API key file under the home directory, the one the
	// jev CLI also reads.
	keyHomePath = ".config/credentials/typesafe-ai/apikey"
	// otherUsersPerm is the permission bits granting group or world access.
	otherUsersPerm fs.FileMode = 0o077
	// maxKeyBytes caps how much of the key file is read.
	maxKeyBytes = 4096
)

// credentialPath is keyFile, or the jev CLI's key under the home directory
// when keyFile is empty.
func credentialPath(keyFile string) (string, error) {
	if keyFile != "" {
		return keyFile, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", derrors.Wrap(
			err,
			derrors.CodeCredentialMissing,
			"no home directory holds the jev API key",
		)
	}

	return filepath.Join(home, filepath.FromSlash(keyHomePath)), nil
}

// readKey reads the API key at path. Neither the file nor its directory may
// be accessible to other users; a key they could read is refused unread.
func readKey(path string) (string, error) {
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return "", keyFileError(err, "no jev API key directory at "+filepath.Dir(path))
	}

	if dirInfo.Mode().Perm()&otherUsersPerm != 0 {
		return "", derrors.New(
			derrors.CodeCredentialInsecure,
			"the jev API key directory "+filepath.Dir(
				path,
			)+" is accessible to other users; chmod 700 it",
		)
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		return "", keyFileError(err, "no jev API key at "+path)
	}

	if !fileInfo.Mode().IsRegular() {
		return "", derrors.New(
			derrors.CodeCredentialMissing,
			"the jev API key at "+path+" is not a file",
		)
	}

	if fileInfo.Mode().Perm()&otherUsersPerm != 0 {
		return "", derrors.New(
			derrors.CodeCredentialInsecure,
			"the jev API key at "+path+" is accessible to other users; chmod 600 it",
		)
	}

	file, err := os.Open(path)
	if err != nil {
		return "", keyFileError(err, "no jev API key at "+path)
	}

	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, maxKeyBytes))
	if err != nil {
		return "", derrors.Wrap(
			err,
			derrors.CodeCredentialMissing,
			"reading the jev API key failed",
		)
	}

	key := strings.TrimSpace(string(data))
	if !usableKey(key) {
		return "", derrors.New(
			derrors.CodeCredentialMissing,
			"the jev API key at "+path+" holds no usable key",
		)
	}

	return key, nil
}

// keyFileError reports a missing file as missing, and anything else (a
// permission error, say) with its cause, which names the path but not content.
func keyFileError(err error, missing string) error {
	if errors.Is(err, fs.ErrNotExist) {
		return derrors.New(derrors.CodeCredentialMissing, missing)
	}

	return derrors.Wrap(err, derrors.CodeCredentialMissing, "the jev API key cannot be read")
}

// usableKey reports whether key is one non-empty run of visible ASCII, the
// only thing that can follow "Bearer " in a header.
func usableKey(key string) bool {
	if key == "" {
		return false
	}

	for index := range len(key) {
		if key[index] <= ' ' || key[index] > '~' {
			return false
		}
	}

	return true
}
