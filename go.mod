module gitlab.com/niharokz/kronos

go 1.23

// gopkg.in is blocked in the build sandbox's network; same source from its GitHub mirror.
// Harmless and removable when building from a network that can reach gopkg.in.
replace gopkg.in/yaml.v3 => github.com/go-yaml/yaml/v3 v3.0.1

replace gopkg.in/check.v1 => github.com/go-check/check v0.0.0-20180628173108-788fd7840127

require (
	github.com/robfig/cron/v3 v3.0.1
	gopkg.in/yaml.v3 v3.0.1
)

require github.com/kr/pretty v0.3.1 // indirect
