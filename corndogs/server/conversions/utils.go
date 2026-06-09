package conversions

import "encoding/json"

// CopyStruct copies source into dest by marshaling to JSON and back. Both sides
// share the same field names / json tags (the gorm model and the generated CSIL
// types both use snake_case json tags), so this is a structural copy without any
// protobuf dependency.
func CopyStruct(source interface{}, dest interface{}) error {
	bytes, err := json.Marshal(source)
	if err != nil {
		return err
	}
	return json.Unmarshal(bytes, dest)
}
