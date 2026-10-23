package manualcheck

// SetInstallerOutputRoot points the installer summary at a test directory that stands in for the
// protected output directories below ProgramData, accepts it without the Windows ownership check, which
// TestProtectedInstallerOutputOnWindows covers, and returns the function that restores both.
func SetInstallerOutputRoot(directory string) func() {
	originalRoot, originalVerify := installerOutputRoot, verifyInstallerOutput
	installerOutputRoot = func() (string, error) { return directory, nil }
	verifyInstallerOutput = func(string) error { return nil }
	return func() { installerOutputRoot, verifyInstallerOutput = originalRoot, originalVerify }
}
