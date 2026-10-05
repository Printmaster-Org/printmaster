package settings

import "testing"

func TestAgentUpdateChannelMetadataAndValidation(t *testing.T) {
	t.Parallel()
	for _, channel := range []string{"", "stable", "beta", "dev", "invalid"} {
		settings := DefaultSettings()
		settings.Features.AgentUpdateChannel = channel
		issues := Validate(settings)
		if (len(issues) != 0) != (channel == "invalid") {
			t.Fatalf("%q validation: %+v", channel, issues)
		}
	}
	for _, field := range DefaultSchema().Fields {
		if field.Path == "features.agent_update_channel" {
			if field.Type != FieldTypeSelect || len(field.Enum) != 4 {
				t.Fatalf("channel field: %+v", field)
			}
			return
		}
	}
	t.Fatal("persistent channel missing from Fleet schema")
}
