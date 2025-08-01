package model

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/meshery/meshery/mesheryctl/internal/cli/root/config"
	"github.com/meshery/meshery/mesheryctl/pkg/utils"
	"github.com/meshery/meshery/server/models"
	"github.com/meshery/meshkit/encoding"
	"github.com/meshery/meshkit/errors"
	meshkitRegistryUtils "github.com/meshery/meshkit/registry"
	meshkitutils "github.com/meshery/meshkit/utils"
	schemav1beta1 "github.com/meshery/schemas/models/v1beta1"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

var importModelCmd = &cobra.Command{
	Use:   "import",
	Short: "Import models",
	Long: `Import models by specifying the directory, file, or URL. You can also provide a template JSON file and registrant name
Documentation for models import can be found at https://docs.meshery.io/reference/mesheryctl/model/import`,
	Example: `
// Import model
mesheryctl model import -f [URI]
 
// Import model from a URL to a meshery model
mesheryctl model import -f [URL]

// Import model from an OCI artifact
mesheryctl model import -f [OCI]

// Import model from a tar.gz file
mesheryctl model import -f [path-to-model.tar.gz]

// Import model from a path
mesheryctl model import -f [path-to-model]

// Import model using CSV files
mesheryctl model import -f [path-to-csv-directory]
	`,
	Args: func(cmd *cobra.Command, args []string) error {
		const errMsg = "Usage: mesheryctl model import [ file | filePath | URL ]\nRun 'mesheryctl model import --help' to see detailed help message"
		file, _ := cmd.Flags().GetString("file")
		if file == "" && len(args) == 0 {
			return fmt.Errorf("[ file | filepath | URL ] isn't specified\n\n%v", errMsg)
		} else if len(args) > 1 {
			return fmt.Errorf("too many arguments\n\n%v", errMsg)
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		var path string
		file, _ := cmd.Flags().GetString("file")
		if file != "" {
			path = file
		} else {
			path = args[0]
		}
		if utils.IsValidUrl(path) {
			err := registerModel(nil, nil, nil, "", "urlImport", path, true)
			if err != nil {
				utils.Log.Error(err)
				return nil
			}
			return nil
		}

		hasCSVs := hasCSVs(path)

		if hasCSVs {
			modelcsvpath, componentcsvpath, relationshipcsvpath, err := meshkitRegistryUtils.GetCsv(path)
			if err != nil {
				utils.Log.Infof("%s: %s", utils.BoldString("ERROR"), "Error importing model using CSV files")
				if meshkitErr, ok := err.(*errors.Error); ok {
					if len(meshkitErr.ProbableCause) != 0 {
						utils.Log.Infof("\n  %s:\n  %s", utils.BoldString("PROBABLE CAUSE"), strings.Join(meshkitErr.ProbableCause, ". "))
					}
					if len(meshkitErr.SuggestedRemediation) != 0 {
						utils.Log.Infof("\n  %s:\n  %s", utils.BoldString("SUGGESTED REMEDIATION"), strings.Join(meshkitErr.SuggestedRemediation, ". "))
					}
				} else {
					utils.Log.Error(err)
				}

				return err

			} else {
				modelData, err := os.ReadFile(modelcsvpath)
				if err != nil {
					return utils.ErrFileRead(err)
				}
				componentData, err := os.ReadFile(componentcsvpath)
				if err != nil {
					return utils.ErrFileRead(err)
				}
				relationshipData, err := os.ReadFile(relationshipcsvpath)
				if err != nil {
					return utils.ErrFileRead(err)
				}
				err = registerModel(modelData, componentData, relationshipData, "model.csv", "csv", "", true)
				if err != nil {
					return err
				}
				locationForModel := utils.MesheryFolder + "/models"
				utils.Log.Info("Model can be accessed from ", locationForModel)
				locationForLogs := utils.MesheryFolder + "/logs/registry"
				utils.Log.Info("Logs for the csv generation can be accessed ", locationForLogs)
				return nil
			}
		}

		// if directory doesn't have CSVs, then process it as a meshery model
		info, err := os.Stat(path)
		if err != nil {
			return models.ErrFolderStat(err, path)
		}

		var tarData []byte
		var fileName string

		if info.IsDir() {
			var buf bytes.Buffer
			err = meshkitutils.Compress(path, &buf)
			if err != nil {
				return err
			}
			tarData = buf.Bytes()
			fileName = filepath.Base(path) + ".tar.gz"
		} else {
			fileData, err := os.ReadFile(path)
			if err != nil {
				return utils.ErrFileRead(err)
			}
			tarData = fileData
			fileName = filepath.Base(path)
		}

		err = registerModel(tarData, nil, nil, fileName, "file", "", true)
		if err != nil {
			return err
		}
		return nil
	},
}

func hasCSVs(path string) bool {
	files, err := os.ReadDir(path)
	if err != nil {
		return false
	}

	for _, f := range files {
		if !f.IsDir() && strings.EqualFold(filepath.Ext(f.Name()), ".csv") {
			return true
		}
	}
	return false
}

func registerModel(data []byte, componentData []byte, relationshipData []byte, filename string, dataType string, sourceURI string, register bool) error {
	mctlCfg, err := config.GetMesheryCtl(viper.GetViper())
	if err != nil {
		return err
	}

	baseURL := mctlCfg.GetBaseMesheryURL()
	url := baseURL + "/api/meshmodels/register"
	var importRequest schemav1beta1.ImportRequest
	importRequest.UploadType = dataType
	if dataType == "csv" {
		importRequest.ImportBody.ModelCsv = "data:text/csv;base64," + base64.StdEncoding.EncodeToString(data)
		importRequest.ImportBody.ComponentCsv = "data:text/csv;base64," + base64.StdEncoding.EncodeToString(componentData)
		importRequest.ImportBody.RelationshipCSV = "data:text/csv;base64," + base64.StdEncoding.EncodeToString(relationshipData)

	} else if dataType == "file" {
		importRequest.ImportBody.ModelFile = data
	} else {
		if data != nil {
			err = encoding.Unmarshal(data, &importRequest.ImportBody.Model)
			if err != nil {
				return err
			}
		}
	}
	importRequest.ImportBody.Url = sourceURI
	importRequest.ImportBody.FileName = filename
	importRequest.Register = register
	requestBody, err := json.Marshal(importRequest)
	if err != nil {
		return err
	}

	req, err := utils.NewRequest(http.MethodPost, url, bytes.NewReader(requestBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := utils.MakeRequest(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err = models.ErrDoRequest(err, resp.Request.Method, url)
		return err
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		err = models.ErrDataRead(err, "response body")
		return err
	}
	var response models.RegistryAPIResponse

	if err := encoding.Unmarshal((bodyBytes), &response); err != nil {
		err = models.ErrUnmarshal(err, "response body")
		return err
	}

	displayEntities(&response)

	if len(response.EntityTypeSummary.SuccessfulModels) == 0 {
		return utils.ErrInvalidModel()
	}

	return nil
}

func displayEntities(response *models.RegistryAPIResponse) {
	displaySummary(response)
	ok := displayEmtpyModel(response)
	if !ok {
		return
	}
	displayEntitisIfModel(response)
}
func displayEmtpyModel(response *models.RegistryAPIResponse) bool {
	if len(response.ModelName) != 0 && response.EntityCount.CompCount == 0 && response.EntityCount.RelCount == 0 {
		if response.EntityCount.TotalErrCount == 0 {
			return false
		}
	}
	return true
}

// TO check the case if we were never able to read the file at first palce
func hasExtension(name string) bool {
	extension := filepath.Ext(name)
	return extension != ""
}

func displayEntitisIfModel(response *models.RegistryAPIResponse) {
	var modelsWithoutExtension []string
	var modelsWithExtension []string

	// Separate models into those with and without extensions
	for _, model := range response.ModelName {
		if model != "" {
			if hasExtension(model) {
				modelsWithExtension = append(modelsWithExtension, model)
			} else {
				modelsWithoutExtension = append(modelsWithoutExtension, model)
			}
		}
	}

	// Function to display models and their components, relationships, and entities
	displayModelInfo := func(model string, hasExtension bool) {
		if !hasExtension {
			boldModel := utils.BoldString("MODEL")
			utils.Log.Infof("\n%s: %s", boldModel, model)
		}
		displaySuccessfulComponents(response, model)
		displaySuccessfulRelationships(response, model)
		displayUnsuccessfulEntities(response, model)
	}

	for _, model := range modelsWithoutExtension {
		displayModelInfo(model, false)
	}
	for _, model := range modelsWithExtension {
		displayModelInfo(model, true)
	}
}

func displaySuccessfulComponents(response *models.RegistryAPIResponse, modelName string) {
	if len(response.EntityTypeSummary.SuccessfulComponents) > 0 {
		header := []string{"Component", "Version"}
		rows := [][]string{}

		for _, comp := range response.EntityTypeSummary.SuccessfulComponents {
			displayName, _ := comp["DisplayName"].(string)
			modelDisplayName, _ := comp["Model"].(string)
			modelVersion, _ := comp["Version"].(string)
			if modelDisplayName == modelName {
				rows = append(rows, []string{displayName, modelVersion})

			}
		}
		if len(rows) > 0 {
			fmt.Println("")
			utils.PrintToTable(header, rows)
		}
	}
}
func displaySuccessfulRelationships(response *models.RegistryAPIResponse, model string) {
	if len(response.EntityTypeSummary.SuccessfulRelationships) > 0 {
		header := []string{"From", "To"}
		seen := make(map[string]bool)
		relationshipMap := make(map[string][][]string)

		for _, rel := range response.EntityTypeSummary.SuccessfulRelationships {
			kind := rel["Kind"].(string)
			subtype := rel["Subtype"].(string)
			relationshipType := rel["RelationshipType"].(string)
			modelName := rel["Model"].(string)
			if modelName != model {
				continue
			}
			selectors := rel["Selectors"].([]interface{})
			for _, selector := range selectors {
				selectorMap := selector.(map[string]interface{})
				allow := selectorMap["allow"].(map[string]interface{})
				from := allow["from"].([]interface{})
				to := allow["to"].([]interface{})
				fromComponent := fmt.Sprintf("%s", from[0].(map[string]interface{})["kind"])
				toComponent := fmt.Sprintf("%s", to[0].(map[string]interface{})["kind"])
				key := fmt.Sprintf("%s/%s/%s", kind, subtype, relationshipType)
				if seen[key+fromComponent+toComponent] {
					continue
				}
				seen[key+fromComponent+toComponent] = true
				relationshipMap[key] = append(relationshipMap[key], []string{fromComponent, toComponent})
			}
		}
		for key, rows := range relationshipMap {
			if len(rows) > 0 {
				fmt.Println("")
				boldRelationships := utils.BoldString("RELATIONSHIP:")
				if len(rows) > 1 {
					boldRelationships = utils.BoldString("RELATIONSHIPS:")
				}
				parts := strings.Split(key, "/")
				utils.Log.Infof("  %s Kind of %s, sub type %s and type %s", boldRelationships, parts[0], parts[1], parts[2])
				utils.PrintToTable(header, rows)
			}
		}
	}
}

func displayUnsuccessfulEntities(response *models.RegistryAPIResponse, modelName string) {
	if len(response.EntityTypeSummary.UnsuccessfulEntityNameWithError) > 0 {
		for _, entity := range response.EntityTypeSummary.UnsuccessfulEntityNameWithError {
			entityMap, err := meshkitutils.Cast[map[string]interface{}](entity)
			if err != nil {
				utils.Log.Error(err)
				continue
			}

			names, err := meshkitutils.Cast[[]interface{}](entityMap["name"])
			if err != nil {
				utils.Log.Error(err)
				continue
			}

			entityTypes, err := meshkitutils.Cast[[]interface{}](entityMap["entityType"])
			if err != nil {
				utils.Log.Error(err)
				continue
			}

			errorDetails, err := meshkitutils.Cast[map[string]interface{}](entityMap["error"])
			if err != nil || len(errorDetails) == 0 {
				utils.Log.Error(err)
				continue
			}

			longDescription := buildDescription(errorDetails["LongDescription"], "LongDescription")
			probableCause := buildDescriptionList(errorDetails["ProbableCause"], "ProbableCause")
			suggestedRemediation := buildDescriptionList(errorDetails["SuggestedRemediation"], "SuggestedRemediation")

			EntityTypeLine := buildEntityTypeLine(names, entityTypes, longDescription, probableCause, suggestedRemediation, modelName)
			if EntityTypeLine != "" {
				fmt.Println("")
				utils.Log.Infof("  %s: Import did not occur for%s error: \n  %s", utils.BoldString("ERROR"), EntityTypeLine, longDescription)
			}

		}
	}
}

func buildDescription(descriptionInterface interface{}, descriptionType string) string {
	descriptionSlice, ok := descriptionInterface.([]interface{})
	if !ok {
		utils.Log.Infof("Type assertion to []interface{} failed for %s: %v (type %T)", descriptionType, descriptionInterface, descriptionInterface)
		return ""
	}

	var description string
	for _, item := range descriptionSlice {
		str, ok := item.(string)
		if !ok {
			utils.Log.Infof("Item in %s is not a string: %v (type %T)", descriptionType, item, item)
			continue
		}
		description += str + " "
	}

	return strings.TrimSpace(description)
}

func buildDescriptionList(descriptionInterface interface{}, descriptionType string) string {
	descriptionSlice, ok := descriptionInterface.([]interface{})
	if !ok {
		utils.Log.Infof("Type assertion to []interface{} failed for %s: %v (type %T)", descriptionType, descriptionInterface, descriptionInterface)
		return ""
	}

	var descriptionList string
	for _, item := range descriptionSlice {
		str, ok := item.(string)
		if !ok {
			utils.Log.Infof("Item in %s is not a string: %v (type %T)", descriptionType, item, item)
			continue
		}
		descriptionList += fmt.Sprintf("  - %s\n", str)
	}

	return strings.TrimSpace(descriptionList)
}

func buildEntityTypeLine(names, entityTypes []interface{}, longDescription, probableCause, suggestedRemediation, modelName string) string {
	compCount, relCount := 0, 0
	EntityTypeLine := ""
	for i, name := range names {
		entityType := ""
		if i < len(entityTypes) {
			entityType = entityTypes[i].(string)
		}
		if modelName != "" {
			if modelName != name.(string) {
				continue
			}
		} else if modelName == "" {
			if entityType != "Unknown" {
				continue
			}
		}
		if entityType == "unknown" {
			utils.Log.Infof("\n%s: Error encountered while importing model %s: \n    %s\n\n    Ensure that you are importing an existing model.\n    Create a new model to import or find an existing model in the Meshery \x1b]8;;https://meshery.io/catalog/models\x1b\\catalog\x1b]8;;\x1b\\.", utils.BoldString("ERROR"), name.(string), longDescription)
			if probableCause != "" {
				utils.Log.Infof("\n  %s:\n  %s", utils.BoldString("PROBABLE CAUSE"), probableCause)
			}
			if suggestedRemediation != "" {
				utils.Log.Infof("\n  %s:\n  %s", utils.BoldString("SUGGESTED REMEDIATION"), suggestedRemediation)
			}
		} else if entityType == "component" {
			compCount++
		} else if entityType == "relationship" {
			relCount++
		}

	}
	if compCount > 0 {
		word := "entity"
		if compCount > 1 {
			word = "entities"
		}
		msg := fmt.Sprintf(" %d %s of type component", compCount, word)
		EntityTypeLine = msg
	}
	if compCount > 0 && relCount > 0 {
		EntityTypeLine = fmt.Sprintf("%s and", EntityTypeLine)
	}
	if relCount > 0 {
		word := "entity"
		if relCount > 1 {
			word = "entities"
		}
		msg := fmt.Sprintf(" %d %s of type relationship", relCount, word)
		EntityTypeLine = fmt.Sprintf("%s%s", EntityTypeLine, msg)
	}
	return EntityTypeLine
}

func displaySummary(response *models.RegistryAPIResponse) {
	if response.EntityCount.ModelCount == 0 {
		return
	}
	boldSummary := utils.BoldString("SUMMARY")
	utils.Log.Infof("%s: %s", boldSummary, response.ErrMsg)
}
func ModelNames(response *models.RegistryAPIResponse) string {
	var builder strings.Builder
	seen := make(map[string]bool)

	for _, model := range response.ModelName {
		if model != "" {
			if !seen[model] {
				if builder.Len() > 0 {
					builder.WriteString(", ")
				}
				builder.WriteString(model)
				seen[model] = true
			}
		}
	}
	return builder.String()
}
func init() {
	importModelCmd.Flags().SetNormalizeFunc(func(f *pflag.FlagSet, name string) pflag.NormalizedName {
		return pflag.NormalizedName(strings.ToLower(name))
	})

	importModelCmd.Flags().StringP("file", "f", "", "Specify path to the file or directory")

}
