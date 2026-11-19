//go:build windows

package history

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
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
func identifyHistoryProcess(p Process) Process {
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

var (
	wintrust                                 = windows.NewLazySystemDLL("wintrust.dll")
	procCryptCATAdminAcquireContext2         = wintrust.NewProc("CryptCATAdminAcquireContext2")
	procCryptCATAdminReleaseContext          = wintrust.NewProc("CryptCATAdminReleaseContext")
	procCryptCATAdminCalcHashFromFileHandle2 = wintrust.NewProc("CryptCATAdminCalcHashFromFileHandle2")
	procCryptCATAdminEnumCatalogFromHash     = wintrust.NewProc("CryptCATAdminEnumCatalogFromHash")
	procCryptCATAdminReleaseCatalogContext   = wintrust.NewProc("CryptCATAdminReleaseCatalogContext")
	procCryptCATCatalogInfoFromContext       = wintrust.NewProc("CryptCATCatalogInfoFromContext")
	procWTHelperProvDataFromStateData        = wintrust.NewProc("WTHelperProvDataFromStateData")
	procWTHelperGetProvSignerFromChain       = wintrust.NewProc("WTHelperGetProvSignerFromChain")
	procWTHelperGetProvCertFromChain         = wintrust.NewProc("WTHelperGetProvCertFromChain")
)

const wtdChoiceCatalog = 2

// wintrustCatalogInfo is WINTRUST_CATALOG_INFO.
type wintrustCatalogInfo struct {
	Size                 uint32
	CatalogVersion       uint32
	CatalogFilePath      *uint16
	MemberTag            *uint16
	MemberFilePath       *uint16
	MemberFile           windows.Handle
	CalculatedFileHash   *byte
	CalculatedFileHashSz uint32
	CatalogContext       uintptr
	CatAdmin             windows.Handle
}

// catalogInfo is CATALOG_INFO.
type catalogInfo struct {
	Size        uint32
	CatalogFile [windows.MAX_PATH]uint16
}

// cryptProviderCert is the start of CRYPT_PROVIDER_CERT.
type cryptProviderCert struct {
	Size uint32
	Cert *windows.CertContext
}

// verifyHistoryExecutable checks the file's own Authenticode signature and, when it has none,
// the catalogs Windows uses to sign most of its own programs, such as cmd.exe and conhost.exe.
func verifyHistoryExecutable(path string) historyIdentity {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return historyIdentity{}
	}
	file := windows.WinTrustFileInfo{Size: uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})), FilePath: path16}
	signed, publisher := verifyTrust(windows.WTD_CHOICE_FILE, unsafe.Pointer(&file))
	if !signed {
		signed, publisher = verifyCatalogSignature(path16)
	}
	return historyIdentity{signed: &signed, publisher: publisher}
}

// verifyTrust runs WinVerifyTrust and, when the signature is valid, names its signer.
func verifyTrust(choice uint32, info unsafe.Pointer) (bool, string) {
	data := windows.WinTrustData{
		Size:                            uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:                        windows.WTD_UI_NONE,
		RevocationChecks:                windows.WTD_REVOKE_NONE,
		UnionChoice:                     choice,
		FileOrCatalogOrBlobOrSgnrOrCert: info,
		StateAction:                     windows.WTD_STATEACTION_VERIFY,
		ProvFlags:                       windows.WTD_REVOCATION_CHECK_NONE | windows.WTD_CACHE_ONLY_URL_RETRIEVAL,
	}
	verifyErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, &data)
	publisher := ""
	if verifyErr == nil {
		publisher = trustSigner(data.StateData)
	}
	data.StateAction = windows.WTD_STATEACTION_CLOSE
	_ = windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, &data)
	return verifyErr == nil, publisher
}

// trustSigner names the certificate that signed the file or its catalog, which is the first
// certificate in the signer's chain.
func trustSigner(state windows.Handle) string {
	provider, _, _ := procWTHelperProvDataFromStateData.Call(uintptr(state))
	if provider == 0 {
		return ""
	}
	signer, _, _ := procWTHelperGetProvSignerFromChain.Call(provider, 0, 0, 0)
	if signer == 0 {
		return ""
	}
	certPtr, _, _ := procWTHelperGetProvCertFromChain.Call(signer, 0)
	if certPtr == 0 {
		return ""
	}
	// certPtr points into memory WinVerifyTrust owns until its state is closed.
	cert := (*(**cryptProviderCert)(unsafe.Pointer(&certPtr))).Cert
	if cert == nil {
		return ""
	}
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

// verifyCatalogSignature looks the file's hash up in the system catalogs, trying SHA-256,
// which current catalogs use, before SHA-1 for older ones.
func verifyCatalogSignature(path *uint16) (bool, string) {
	f, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return false, ""
	}
	defer windows.CloseHandle(f)
	sha256, _ := windows.UTF16PtrFromString("SHA256")
	for _, algorithm := range []*uint16{sha256, nil} {
		if signed, publisher := verifyCatalogHash(f, path, algorithm); signed {
			return true, publisher
		}
	}
	return false, ""
}

func verifyCatalogHash(f windows.Handle, path, algorithm *uint16) (bool, string) {
	var admin windows.Handle
	if ok, _, _ := procCryptCATAdminAcquireContext2.Call(uintptr(unsafe.Pointer(&admin)), 0, uintptr(unsafe.Pointer(algorithm)), 0, 0); ok == 0 {
		return false, ""
	}
	defer procCryptCATAdminReleaseContext.Call(uintptr(admin), 0)
	if _, err := windows.Seek(f, 0, 0); err != nil {
		return false, ""
	}
	var hash [64]byte
	size := uint32(len(hash))
	if ok, _, _ := procCryptCATAdminCalcHashFromFileHandle2.Call(uintptr(admin), uintptr(f), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&hash[0])), 0); ok == 0 || size == 0 || size > uint32(len(hash)) {
		return false, ""
	}
	catalog, _, _ := procCryptCATAdminEnumCatalogFromHash.Call(uintptr(admin), uintptr(unsafe.Pointer(&hash[0])), uintptr(size), 0, 0)
	if catalog == 0 {
		return false, ""
	}
	defer procCryptCATAdminReleaseCatalogContext.Call(uintptr(admin), catalog, 0)
	info := catalogInfo{Size: uint32(unsafe.Sizeof(catalogInfo{}))}
	if ok, _, _ := procCryptCATCatalogInfoFromContext.Call(catalog, uintptr(unsafe.Pointer(&info)), 0); ok == 0 {
		return false, ""
	}
	tag, _ := windows.UTF16PtrFromString(strings.ToUpper(hex.EncodeToString(hash[:size])))
	member := wintrustCatalogInfo{
		Size:                 uint32(unsafe.Sizeof(wintrustCatalogInfo{})),
		CatalogFilePath:      &info.CatalogFile[0],
		MemberTag:            tag,
		MemberFilePath:       path,
		MemberFile:           f,
		CalculatedFileHash:   &hash[0],
		CalculatedFileHashSz: size,
		CatAdmin:             admin,
	}
	return verifyTrust(wtdChoiceCatalog, unsafe.Pointer(&member))
}
