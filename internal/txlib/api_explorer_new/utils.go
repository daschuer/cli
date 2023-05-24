package api_explorer_new

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"text/template"

	"github.com/ktr0731/go-fuzzyfinder"
	"github.com/transifex/cli/internal/txlib"
	"github.com/transifex/cli/internal/txlib/config"
	"github.com/transifex/cli/pkg/jsonapi"
	"github.com/urfave/cli/v2"
)

func getApi(c *cli.Context) (*jsonapi.Connection, error) {
	cfg, err := config.LoadFromPaths(
		c.String("root-config"),
		c.String("config"),
	)
	if err != nil {
		return nil, fmt.Errorf("error loading configuration: %s", err)
	}
	hostname, token, err := txlib.GetHostAndToken(
		&cfg,
		c.String("hostname"),
		c.String("token"),
	)
	if err != nil {
		return nil, fmt.Errorf("error getting API token: %s", err)
	}

	client, err := txlib.GetClient(c.String("cacert"))
	if err != nil {
		return nil, fmt.Errorf("error getting HTTP client configuration: %s", err)
	}

	return &jsonapi.Connection{
		Host:    hostname,
		Token:   token,
		Client:  client,
		Headers: map[string]string{"Integration": "txclient"},
	}, nil
}

func save(key, value string) error {
	if _, err := os.Stat(".tx"); os.IsNotExist(err) {
		err := os.Mkdir(".tx", 0755)
		if err != nil {
			return err
		}
	}
	var body []byte
	if _, err := os.Stat(".tx/api_explorer_session.json"); err == nil {
		body, err = os.ReadFile(".tx/api_explorer_session.json")
		if err != nil {
			return err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		body = []byte("{}")

	} else {
		return err
	}
	var data map[string]string
	err := json.Unmarshal(body, &data)
	if err != nil {
		return err
	}
	data[key] = value
	body, err = json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	err = os.WriteFile(".tx/api_explorer_session.json", body, 0644)
	if err != nil {
		return err
	}
	return nil
}

func load(key string) (string, error) {
	_, err := os.Stat(".tx/api_explorer_session.json")
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	body, err := os.ReadFile(".tx/api_explorer_session.json")
	if err != nil {
		return "", err
	}
	var data map[string]string
	err = json.Unmarshal(body, &data)
	if err != nil {
		return "", err
	}
	value, exists := data[key]
	if !exists {
		return "", nil
	}
	return value, nil
}

func clear(key string) error {
	_, err := os.Stat(".tx/api_explorer_session.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	body, err := os.ReadFile(".tx/api_explorer_session.json")
	if err != nil {
		return err
	}
	var data map[string]string
	err = json.Unmarshal(body, &data)
	if err != nil {
		return err
	}
	delete(data, key)
	body, err = json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	err = os.WriteFile(".tx/api_explorer_session.json", body, 0644)
	if err != nil {
		return err
	}
	fmt.Printf("Cleared %s from session file\n", key)
	return nil
}

func handlePagination(body []byte) error {
	var payload struct {
		Links struct {
			Next     string
			Previous string
		}
	}
	err := json.Unmarshal(body, &payload)
	if err != nil {
		return err
	}
	if payload.Links.Next != "" {
		err = save("next", payload.Links.Next)
		if err != nil {
			return err
		}
	} else {
		clear("next")
	}
	if payload.Links.Previous != "" {
		err = save("previous", payload.Links.Previous)
		if err != nil {
			return err
		}
	} else {
		clear("previous")
	}
	return nil
}

func invokePager(pager string, body []byte) error {
	var unmarshalled map[string]interface{}
	err := json.Unmarshal(body, &unmarshalled)
	if err != nil {
		return err
	}
	output, err := json.MarshalIndent(unmarshalled, "", "  ")
	if err != nil {
		return err
	}
	cmd := exec.Command(pager)
	cmd.Stdin = bytes.NewBuffer(output)
	cmd.Stdout = os.Stdout
	err = cmd.Run()
	if err != nil {
		return err
	}
	return nil
}

func invokeEditor(input []byte, editor string) ([]byte, error) {
	tempFile, err := os.CreateTemp("", "*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tempFile.Name())
	_, err = tempFile.Write(input)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(editor, tempFile.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	err = cmd.Run()
	if err != nil {
		return nil, err
	}
	_, err = tempFile.Seek(0, 0)
	if err != nil {
		return nil, err
	}
	output, err := io.ReadAll(tempFile)
	if err != nil {
		return nil, err
	}
	return output, nil
}

func joinPages(api *jsonapi.Connection, bodyBytes []byte) ([]byte, error) {
	var resultJson struct {
		Data []interface{} `json:"data"`
	}
	var bodyJson struct {
		Data  []interface{} `json:"data"`
		Links struct {
			Next string `json:"next"`
		} `json:"links"`
	}
	err := json.Unmarshal(bodyBytes, &bodyJson)
	if err != nil {
		return nil, err
	}
	resultJson.Data = append(resultJson.Data, bodyJson.Data...)
	for bodyJson.Links.Next != "" {
		bodyBytes, err = api.ListBodyFromPath(bodyJson.Links.Next)
		if err != nil {
			return nil, err
		}
		bodyJson.Links.Next = ""
		err = json.Unmarshal(bodyBytes, &bodyJson)
		if err != nil {
			return nil, err
		}
		resultJson.Data = append(resultJson.Data, bodyJson.Data...)
	}
	resultBody, err := json.Marshal(resultJson)
	if err != nil {
		return nil, err
	}
	return resultBody, nil
}

func getIsEmpty(bodyBytes []byte) (bool, error) {
	var bodyJson struct {
		Data []interface{} `json:"data"`
	}
	err := json.Unmarshal(bodyBytes, &bodyJson)
	if err != nil {
		return false, err
	}
	if len(bodyJson.Data) == 0 {
		return true, nil
	}
	return false, nil
}

func getIfOnlyOne(bodyBytes []byte) (string, error) {
	var bodyJson struct {
		Data []struct {
			Id string `json:"id"`
		} `json:"data"`
	}
	err := json.Unmarshal(bodyBytes, &bodyJson)
	if err != nil {
		return "", err
	}
	if len(bodyJson.Data) == 1 {
		return bodyJson.Data[0].Id, nil
	}
	return "", nil
}

func fuzzy(
	api *jsonapi.Connection,
	body []byte,
	header string,
	display string,
	allowEmpty bool,
	multi bool,
) ([]string, error) {
	var payload map[string]interface{}
	err := json.Unmarshal(body, &payload)
	if err != nil {
		return nil, err
	}
	items, err := jsonapi.PostProcessListResponse(api, body)
	if err != nil {
		return nil, err
	}

	var data []jsonapi.Resource
	if allowEmpty {
		data = append([]jsonapi.Resource{{}}, items.Data...)
	} else {
		data = append([]jsonapi.Resource{}, items.Data...)
	}

	displayFunc := func(i int) string {
		if allowEmpty && i == 0 {
			return "<empty>"
		}
		obj := data[i]
		result, err := renderTemplate(display, obj)
		if err != nil {
			return obj.Id
		}
		return result
	}

	previewOption := fuzzyfinder.WithPreviewWindow(func(i, w, h int) string {
		if i == -1 {
			return ""
		}
		if allowEmpty && i == 0 {
			return "Empty selection"
		}
		var idx int
		if allowEmpty {
			idx = i - 1
		} else {
			idx = i
		}
		item, err := json.MarshalIndent(
			payload["data"].([]interface{})[idx],
			"",
			"  ",
		)
		if err != nil {
			return ""
		}
		return string(item)
	})

	var indices []int
	if multi {
		indices, err = fuzzyfinder.FindMulti(
			data, displayFunc, previewOption, fuzzyfinder.WithHeader(header),
		)
	} else {
		index, err := fuzzyfinder.Find(
			data, displayFunc, previewOption, fuzzyfinder.WithHeader(header),
		)
		if err != nil {
			return nil, err
		}
		indices = append(indices, index)
	}
	var ids []string
	for _, index := range indices {
		ids = append(ids, data[index].Id)
	}
	return ids, nil
}

func edit(editor string, item *jsonapi.Resource, editable_fields []string) error {
	var preAttributes map[string]interface{}
	err := item.MapAttributes(&preAttributes)
	if err != nil {
		return err
	}
	for field := range preAttributes {
		if !stringSliceContains(editable_fields, field) {
			delete(preAttributes, field)
		}
	}
	body, err := json.MarshalIndent(preAttributes, "", "  ")
	if err != nil {
		return err
	}
	body, err = invokeEditor(body, editor)
	if err != nil {
		return err
	}
	var postAttributes map[string]interface{}
	err = json.Unmarshal(body, &postAttributes)
	if err != nil {
		return err
	}
	var finalFields []string
	for field, postValue := range postAttributes {
		preValue, exists := preAttributes[field]
		if !exists || reflect.DeepEqual(preValue, postValue) {
			delete(postAttributes, field)
		} else {
			finalFields = append(finalFields, field)
		}
	}
	if len(finalFields) == 0 {
		return errors.New("nothing changed")
	}
	item.Attributes = postAttributes
	err = item.Save(finalFields)
	if err != nil {
		return err
	}
	return nil
}

func renderTemplate(templateString string, context interface{}) (string, error) {
	t := template.New("")
	t, err := t.Parse(templateString)
	if err != nil {
		return "", err
	}
	buf := bytes.NewBufferString("")
	err = t.Execute(buf, context)
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

func stringSliceContains(haystack []string, needle string) bool {
	for _, key := range haystack {
		if key == needle {
			return true
		}
	}
	return false
}
