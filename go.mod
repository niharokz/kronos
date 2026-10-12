module gitlab.com/niharokz/kronos

go 1.23

// yaml.v3 is fetched from its GitHub mirror (same source as gopkg.in) so the build
// only needs github.com; drop these two lines to fetch from gopkg.in directly.
replace gopkg.in/yaml.v3 => github.com/go-yaml/yaml/v3 v3.0.1

replace gopkg.in/check.v1 => github.com/go-check/check v0.0.0-20180628173108-788fd7840127

require (
	github.com/robfig/cron/v3 v3.0.1
	gopkg.in/yaml.v3 v3.0.1
)

require github.com/kr/pretty v0.3.1 // indirect
