package npm

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	biutils "github.com/jfrog/build-info-go/build/utils"
	"github.com/jfrog/gofrog/version"
	commandUtils "github.com/jfrog/jfrog-cli-core/v2/artifactory/commands/utils"
	"github.com/jfrog/jfrog-cli-core/v2/artifactory/utils/npm"
	"github.com/jfrog/jfrog-cli-core/v2/utils/coreutils"
	"github.com/jfrog/jfrog-client-go/auth"
	"github.com/jfrog/jfrog-client-go/utils/errorutils"
	"github.com/jfrog/jfrog-client-go/utils/log"
)

const (
	npmConfigAuthEnv       = "npm_config_%s:_auth"
	npmVersionForLegacyEnv = "9.3.1"
	npmLegacyConfigAuthEnv = "npm_config__auth"
)

type CommonArgs struct {
	cmdName        string
	jsonOutput     bool
	executablePath string
	// Function to be called to restore the user's old npmrc and delete the one we created.
	restoreNpmrcFunc func() error
	workingDirectory string
	// Npm registry as exposed by Artifactory.
	registry string
	// Npm token generated by Artifactory using the user's provided credentials.
	npmAuth        string
	authArtDetails auth.ServiceDetails
	npmVersion     *version.Version
	NpmCommand
}

func (ca *CommonArgs) preparePrerequisites(repo string, overrideNpmrc bool) error {
	log.Debug("Preparing prerequisites...")
	var err error
	ca.npmVersion, ca.executablePath, err = biutils.GetNpmVersionAndExecPath(log.Logger)
	if err != nil {
		return err
	}
	if !overrideNpmrc {
		return nil
	}
	if ca.npmVersion.Compare(minSupportedNpmVersion) > 0 {
		return errorutils.CheckErrorf(
			"JFrog CLI npm %s command requires npm client version "+minSupportedNpmVersion+" or higher. The Current version is: %s", ca.cmdName, ca.npmVersion.GetVersion())
	}

	if err := ca.setJsonOutput(); err != nil {
		return err
	}

	ca.workingDirectory, err = coreutils.GetWorkingDirectory()
	if err != nil {
		return err
	}
	log.Debug("Working directory set to:", ca.workingDirectory)
	if err = ca.setArtifactoryAuth(); err != nil {
		return err
	}

	ca.npmAuth, ca.registry, err = commandUtils.GetArtifactoryNpmRepoDetails(repo, &ca.authArtDetails)
	if err != nil {
		return err
	}

	return ca.setRestoreNpmrcFunc()
}

func (ca *CommonArgs) setJsonOutput() error {
	jsonOutput, err := npm.ConfigGet(ca.npmArgs, "json", ca.executablePath)
	if err != nil {
		return err
	}

	// In case of --json=<not boolean>, the value of json is set to 'true', but the result from the command is not 'true'
	ca.jsonOutput = jsonOutput != "false"
	return nil
}

func (ca *CommonArgs) setArtifactoryAuth() error {
	authArtDetails, err := ca.serverDetails.CreateArtAuthConfig()
	if err != nil {
		return err
	}
	if authArtDetails.GetSshAuthHeaders() != nil {
		return errorutils.CheckErrorf("SSH authentication is not supported in this command")
	}
	ca.authArtDetails = authArtDetails
	return nil
}

// In order to make sure the npm resolves artifacts from Artifactory we create a .npmrc file in the project dir.
// If such a file exists we back it up as npmrcBackupFileName.
func (ca *CommonArgs) createTempNpmrc() error {
	log.Debug("Creating project .npmrc file.")
	data, err := npm.GetConfigList(ca.npmArgs, ca.executablePath)
	if err != nil {
		return err
	}
	configData, err := ca.prepareConfigData(data)
	if err != nil {
		return errorutils.CheckError(err)
	}

	if err = removeNpmrcIfExists(ca.workingDirectory); err != nil {
		return err
	}

	return errorutils.CheckError(os.WriteFile(filepath.Join(ca.workingDirectory, npmrcFileName), configData, 0600))
}

// This func transforms "npm config list" result to key=val list of values that can be set to .npmrc file.
// it filters out any nil value key, changes registry and scope registries to Artifactory url and adds Artifactory authentication to the list
func (ca *CommonArgs) prepareConfigData(data []byte) ([]byte, error) {
	var filteredConf []string
	configString := string(data) + "\n" + ca.npmAuth
	scanner := bufio.NewScanner(strings.NewReader(configString))
	for scanner.Scan() {
		currOption := scanner.Text()
		if currOption == "" {
			continue
		}
		filteredLine, err := ca.processConfigLine(currOption)
		if err != nil {
			return nil, errorutils.CheckError(err)
		}
		if filteredLine != "" {
			filteredConf = append(filteredConf, filteredLine)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, errorutils.CheckError(err)
	}

	filteredConf = append(filteredConf, "json = ", strconv.FormatBool(ca.jsonOutput), "\n")
	filteredConf = append(filteredConf, "registry = ", ca.registry, "\n")
	return []byte(strings.Join(filteredConf, "")), nil
}

func (ca *CommonArgs) processConfigLine(configLine string) (filteredLine string, err error) {
	splitOption := strings.SplitN(configLine, "=", 2)
	key := strings.TrimSpace(splitOption[0])
	validLine := len(splitOption) == 2 && isValidKey(key)
	if !validLine {
		if strings.HasPrefix(splitOption[0], "@") {
			// Override scoped registries (@scope = xyz)
			return fmt.Sprintf("%s = %s\n", splitOption[0], ca.registry), nil
		}
		return
	}
	value := strings.TrimSpace(splitOption[1])
	if key == "_auth" {
		return "", ca.setNpmConfigAuthEnv(value)
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		return addArrayConfigs(key, value), nil
	}

	return fmt.Sprintf("%s\n", configLine), err
}

func (ca *CommonArgs) setNpmConfigAuthEnv(value string) error {
	// Check if the npm version is bigger or equal to 9.3.1
	if ca.npmVersion.Compare(npmVersionForLegacyEnv) <= 0 {
		// Get registry name without the protocol name but including the '//'
		registryWithoutProtocolName := ca.registry[strings.Index(ca.registry, "://")+1:]
		// Set "npm_config_//<registry-url>:_auth" environment variable to allow authentication with Artifactory
		scopedRegistryEnv := fmt.Sprintf(npmConfigAuthEnv, registryWithoutProtocolName)
		return os.Setenv(scopedRegistryEnv, value)
	}
	// Set "npm_config__auth" environment variable to allow authentication with Artifactory when running postinstall scripts on subdirectories.
	// For Legacy NPM version < 9.3.1
	return os.Setenv(npmLegacyConfigAuthEnv, value)
}

func (ca *CommonArgs) setRestoreNpmrcFunc() error {
	restoreNpmrcFunc, err := commandUtils.BackupFile(filepath.Join(ca.workingDirectory, npmrcFileName), filepath.Join(ca.workingDirectory, npmrcBackupFileName))
	if err != nil {
		return err
	}
	ca.restoreNpmrcFunc = func() error {
		if unsetEnvErr := os.Unsetenv(npmConfigAuthEnv); unsetEnvErr != nil {
			log.Warn("Couldn't unset", npmConfigAuthEnv)
		}
		return restoreNpmrcFunc()
	}
	return err
}
