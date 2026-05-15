package version

var Version = "dev"

func ReleaseTag() string {
	if Version == "" || Version == "dev" {
		return "latest"
	}
	return Version
}
