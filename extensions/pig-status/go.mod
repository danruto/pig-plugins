module github.com/danruto/pig-plugins/extensions/pig-status

go 1.26

require (
	github.com/MichaelKinsy/PiG/extensions/sdk v0.0.0
	github.com/danruto/pig-plugins/extensions/pig-status/nativeusage v0.0.0
)

replace github.com/danruto/pig-plugins/extensions/pig-status/nativeusage => ./nativeusage
