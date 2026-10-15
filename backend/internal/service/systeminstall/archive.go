package systeminstall

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
)

// Pinned official releases. Debian does not ship gh or glab, and AO does not
// ask a desktop user for an administrator password, so Linux installs a
// checksummed archive instead of apt. Keep deploy/headless/Dockerfile on the
// same versions.
const (
	ghVersion   = "2.102.0"
	glabVersion = "1.123.0"
)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

type linuxArchive struct {
	binary string
	sha256 map[string]string
	urlFor func(arch string) string
}

var ghLinuxArchive = linuxArchive{
	binary: "gh",
	sha256: map[string]string{
		"amd64": "bb766f710eef8ede859c18578c72c327597cd4c8a85b06001b1f3843c6019386",
		"arm64": "7862c86c72f43df3a2d93ddde6f473285b4e2af61b494849846827e513ef6484",
	},
	urlFor: func(arch string) string {
		return fmt.Sprintf("https://github.com/cli/cli/releases/download/v%s/gh_%s_linux_%s.tar.gz", ghVersion, ghVersion, arch)
	},
}

var glabLinuxArchive = linuxArchive{
	binary: "glab",
	sha256: map[string]string{
		"amd64": "9c9ac4c2b74ec8709dec4819a4041050f3680c6aaa18f0ed30434081f4fe9bef",
		"arm64": "1f2d0a885b28d08de341a597b2eb7204ce03d4e67b008efecab9951e4b014f0c",
	},
	urlFor: func(arch string) string {
		return fmt.Sprintf("https://gitlab.com/api/v4/projects/gitlab-org%%2Fcli/packages/generic/glab/%s/glab_%s_linux_%s.tar.gz", glabVersion, glabVersion, arch)
	},
}

func (s *Service) planOfficialArchive(target Target, rel linuxArchive) Plan {
	if _, err := s.executables.LookPath("curl"); err != nil {
		return Plan{Target: target, Unsupported: true, Method: "official-archive", Reason: "curl was not found on PATH."}
	}
	if _, err := s.executables.LookPath("tar"); err != nil {
		return Plan{Target: target, Unsupported: true, Method: "official-archive", Reason: "tar was not found on PATH."}
	}
	arch := linuxArch(runtime.GOARCH)
	sha := rel.sha256[arch]
	if arch == "" || !sha256Hex.MatchString(sha) {
		return Plan{Target: target, Unsupported: true, Method: "official-archive", Reason: "This CPU architecture is not supported for the official archive."}
	}
	dest := archiveInstallDir()
	binaryPath := filepath.Join(dest, rel.binary)
	return Plan{
		Target:              target,
		Command:             []string{"/bin/sh", "-c", officialArchiveScript(rel.urlFor(arch), sha, rel.binary, dest)},
		Method:              "official-archive",
		Package:             rel.binary + " " + archiveVersion(rel),
		ExpectedDestination: binaryPath,
	}
}

func archiveVersion(rel linuxArchive) string {
	if rel.binary == "glab" {
		return glabVersion
	}
	return ghVersion
}

func linuxArch(goarch string) string {
	switch goarch {
	case "amd64", "arm64":
		return goarch
	default:
		return ""
	}
}

// archiveInstallDir prefers /usr/local/bin when this process can write it
// (the headless container runs as root). Otherwise the binary lands in
// ~/.local/bin, which the headless entrypoint already puts on PATH.
func archiveInstallDir() string {
	if dirWritable("/usr/local/bin") {
		return "/usr/local/bin"
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "/tmp"
	}
	return filepath.Join(home, ".local", "bin")
}

func dirWritable(dir string) bool {
	file, err := os.CreateTemp(dir, ".ao-write-*")
	if err != nil {
		return false
	}
	name := file.Name()
	_ = file.Close()
	_ = os.Remove(name)
	return true
}

func officialArchiveScript(url, sha, name, dest string) string {
	binaryPath := filepath.Join(dest, name)
	return fmt.Sprintf(`set -eu
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL %q -o "$tmp/archive.tar.gz"
cd "$tmp"
echo '%s  archive.tar.gz' | sha256sum -c -
tar -xzf archive.tar.gz
bin=$(find "$tmp" -type f -name %q | head -n 1)
test -n "$bin"
mkdir -p %q
cp "$bin" %q
chmod 0755 %q
`, url, sha, name, dest, binaryPath, binaryPath)
}
