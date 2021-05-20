package npm

import (
	"github.com/jfrog/jfrog-cli-core/artifactory/utils"
	"github.com/jfrog/jfrog-cli-core/utils/coreutils"
	"strconv"

	"github.com/jfrog/jfrog-client-go/utils/errorutils"
)

func ExtractNpmOptionsFromArgs(args []string) (threads int, detailedSummary bool, cleanArgs []string, buildConfig *utils.BuildConfiguration, err error) {
	threads = 3
	// Extract threads information from the args.
	flagIndex, valueIndex, numOfThreads, err := coreutils.FindFlag("--threads", args)
	if err != nil {
		return
	}
	coreutils.RemoveFlagFromCommand(&args, flagIndex, valueIndex)
	if numOfThreads != "" {
		threads, err = strconv.Atoi(numOfThreads)
		if err != nil {
			err = errorutils.CheckError(err)
			return
		}
	}

	flagIndex, detailedSummary, err = coreutils.FindBooleanFlag("--detailed-summary", args)
	if err != nil {
		return
	}
	// Since boolean flag might appear as --flag or --flag=value, the value index is the same as the flag index.
	coreutils.RemoveFlagFromCommand(&args, flagIndex, flagIndex)

	cleanArgs, buildConfig, err = utils.ExtractBuildDetailsFromArgs(args)
	return
}
