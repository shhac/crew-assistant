//go:build windows

package cli

import (
	"errors"
	"net"
	"os"
)

const upgradeListenerEnv = "CREW_UPGRADE_LISTEN_FD"

func inheritedUpgradeListener() (net.Listener, error) {
	_ = os.Unsetenv(upgradeListenerEnv)
	return nil, nil
}
func preserveUpgradeListener(net.Listener) (*os.File, error) {
	return nil, errors.New("self-upgrade is not available on Windows")
}
func upgradeListenerDescriptor(*os.File) string { return "" }
func replaceUpgradeProcess(string, []string) error {
	return errors.New("self-upgrade is not available on Windows")
}
func startUpgradeDetached(string, []string, string) error {
	return errors.New("self-upgrade is not available on Windows")
}
func upgradePIDAlive(int) bool { return false }
func killUpgradePID(int) error { return errors.New("self-upgrade is not available on Windows") }

func protectInheritedUpgradeListener() {}

func upgradeProcessIdentity(int) string { return "" }
