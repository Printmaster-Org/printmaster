//go:build windows

package autoupdate

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// These identities must match the WiX product family and executable component.
const (
	agentMSIUpgradeCode = "{E1F8F20A-5D36-4DF8-879B-A868A69243AC}"
	agentMSIComponent   = "{6D585077-C1D7-40DC-87AA-02A7821C4DF1}"
)

var (
	msiDLL                 = windows.NewLazySystemDLL("msi.dll")
	msiEnumRelatedProducts = msiDLL.NewProc("MsiEnumRelatedProductsW")
	msiGetComponentPath    = msiDLL.NewProc("MsiGetComponentPathW")
)

type msiProduct struct {
	code string
	path string
}

func checkMSIInstallation(binaryPath string) (string, bool, error) {
	programFiles, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return "", false, fmt.Errorf("resolve system Program Files directory: %w", err)
	}
	root := filepath.Join(programFiles, "PrintMaster")
	canonical := filepath.Join(root, "Agent", "printmaster-agent.exe")
	legacy := filepath.Join(root, "printmaster-agent.exe")
	if !strings.EqualFold(filepath.Clean(binaryPath), canonical) &&
		!strings.EqualFold(filepath.Clean(binaryPath), legacy) {
		return "", false, nil
	}
	upgrade, err := windows.UTF16PtrFromString(agentMSIUpgradeCode)
	if err != nil {
		return "", false, err
	}
	component, err := windows.UTF16PtrFromString(agentMSIComponent)
	if err != nil {
		return "", false, err
	}
	// Windows Installer requires all enumeration calls on the same thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var products []msiProduct
	for index := uint32(0); ; index++ {
		var product [39]uint16
		result, _, _ := msiEnumRelatedProducts.Call(uintptr(unsafe.Pointer(upgrade)), 0,
			uintptr(index), uintptr(unsafe.Pointer(&product[0])))
		if result == uintptr(windows.ERROR_NO_MORE_ITEMS) {
			break
		}
		if result != 0 {
			return "", false, fmt.Errorf("enumerate Agent MSI products: %w", windows.Errno(result))
		}
		path := make([]uint16, 32768)
		length := uint32(len(path))
		state, _, _ := msiGetComponentPath.Call(uintptr(unsafe.Pointer(&product[0])),
			uintptr(unsafe.Pointer(component)), uintptr(unsafe.Pointer(&path[0])), uintptr(unsafe.Pointer(&length)))
		code := windows.UTF16ToString(product[:])
		// INSTALLSTATE_LOCAL: the component is actually installed on this host.
		if int32(state) != 3 {
			return "", false, fmt.Errorf("agent MSI %s executable component is not locally installed (state %d); repair the installation", code, int32(state))
		}
		products = append(products, msiProduct{code: code, path: windows.UTF16ToString(path)})
	}
	return matchMSIProduct(binaryPath, products)
}

func matchMSIProduct(binaryPath string, products []msiProduct) (string, bool, error) {
	if len(products) == 0 {
		return "", false, nil
	}
	if len(products) != 1 || !strings.EqualFold(filepath.Clean(binaryPath), filepath.Clean(products[0].path)) {
		return "", false, fmt.Errorf("agent MSI registration is ambiguous or does not own executable %q; repair the installation", binaryPath)
	}
	return products[0].code, true, nil
}
