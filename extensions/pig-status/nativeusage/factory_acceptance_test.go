package nativeusage_test

import (
	"testing"

	"github.com/danruto/pig-plugins/extensions/pig-status/nativeusage"
)

func TestExtensionReturnsNativeFactory(t *testing.T) {
	ext := nativeusage.Extension()

	if ext == nil {
		t.Fatal("Extension returned nil")
	}
}
