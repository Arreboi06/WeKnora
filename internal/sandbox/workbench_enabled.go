package sandbox

import (
	"os"
	"strconv"
	"strings"
)

const WorkbenchEnabledEnv = "WEKNORA_SANDBOX_WORKBENCH_ENABLED"

func WorkbenchEnabled() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(WorkbenchEnabledEnv)))
	return err == nil && enabled
}
