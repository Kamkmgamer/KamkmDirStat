//go:build windows

package main

import (
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type historyIdentityKey struct {
	path    string
	size    int64
	modTime int64
}

type historyIdentity struct {
	publisher string
	signed    *bool
}

var historyIdentities = struct {
	sync.Mutex
	items map[historyIdentityKey]historyIdentity
}{items: map[historyIdentityKey]historyIdentity{}}

// identifyHistoryProcess checks the executable on disk. Inaccessible paths stay unknown;
// an explicit false means Windows examined the file but did not verify its signature.
func identifyHistoryProcess(p HistoryProcess) HistoryProcess {
	path := filepath.Clean(p.Cmd)
	if path == "." || path == "" {
		return p
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return p
	}
	key := historyIdentityKey{path: p.executable(), size: info.Size(), modTime: info.ModTime().UnixNano()}
	historyIdentities.Lock()
	identity, ok := historyIdentities.items[key]
	historyIdentities.Unlock()
	if !ok {
		identity = verifyHistoryExecutable(path)
		historyIdentities.Lock()
		if len(historyIdentities.items) >= 2048 {
			historyIdentities.items = map[historyIdentityKey]historyIdentity{}
		}
		historyIdentities.items[key] = identity
		historyIdentities.Unlock()
	}
	p.Publisher = identity.publisher
	p.Signed = identity.signed
	return p
}

func verifyHistoryExecutable(path string) historyIdentity {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return historyIdentity{}
	}
	file := windows.WinTrustFileInfo{Size: uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})), FilePath: path16}
	data := windows.WinTrustData{
		Size:                            uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:                        windows.WTD_UI_NONE,
		RevocationChecks:                windows.WTD_REVOKE_NONE,
		UnionChoice:                     windows.WTD_CHOICE_FILE,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(&file),
		StateAction:                     windows.WTD_STATEACTION_VERIFY,
		ProvFlags:                       windows.WTD_REVOCATION_CHECK_NONE,
	}
	verifyErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, &data)
	data.StateAction = windows.WTD_STATEACTION_CLOSE
	_ = windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, &data)
	signed := verifyErr == nil
	identity := historyIdentity{signed: &signed}
	if signed {
		identity.publisher = historyExecutablePublisher(path16)
	}
	return identity
}

func historyExecutablePublisher(path *uint16) string {
	var store windows.Handle
	err := windows.CryptQueryObject(windows.CERT_QUERY_OBJECT_FILE, unsafe.Pointer(path),
		windows.CERT_QUERY_CONTENT_FLAG_PKCS7_SIGNED_EMBED, windows.CERT_QUERY_FORMAT_FLAG_BINARY,
		0, nil, nil, nil, &store, nil, nil)
	if err != nil {
		return ""
	}
	defer windows.CertCloseStore(store, 0)
	cert, err := windows.CertEnumCertificatesInStore(store, nil)
	if err != nil || cert == nil {
		return ""
	}
	defer windows.CertFreeCertificateContext(cert)
	n := windows.CertGetNameString(cert, windows.CERT_NAME_SIMPLE_DISPLAY_TYPE, 0, nil, nil, 0)
	if n <= 1 {
		return ""
	}
	buf := make([]uint16, n)
	if windows.CertGetNameString(cert, windows.CERT_NAME_SIMPLE_DISPLAY_TYPE, 0, nil, &buf[0], n) <= 1 {
		return ""
	}
	return windows.UTF16ToString(buf)
}
