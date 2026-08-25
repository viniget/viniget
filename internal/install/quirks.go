package install

// installQuirk is the per-package Wine environment needed while an installer
// runs. These settings deliberately end with the installer: an application
// may need a different environment when it is launched.
type installQuirk struct {
	DLLOverrides string
}

// installQuirks contains measured exceptions to the normal installer path.
// A quirk belongs here only when the unmodified manifest and viniget's normal
// type policy fail under Wine, and the smallest working override is known.
var installQuirks = map[string]installQuirk{
	// Obsidian's electron-builder NSIS installer asks PowerShell whether an
	// Obsidian process is running. Wine 11.16's PowerShell stub returns success
	// without evaluating the script, so the installer believes the application
	// cannot be closed and exits 2 before writing a file. Disabling the stub
	// makes electron-builder use its tasklist fallback; the same /S /allusers
	// invocation then installs normally.
	"Obsidian.Obsidian": {DLLOverrides: "powershell.exe=d"},
}

func installQuirkFor(id string) installQuirk { return installQuirks[id] }
