package archetype

type InputOption struct {
	Name  string
	Title string
}

type InputOptions struct {
	inputs []InputOption
}

func (i *InputOptions) Titles() []string {
	var titles []string
	for _, input := range i.inputs {
		titles = append(titles, input.Title)
	}
	return titles
}

func (io *InputOptions) GetName(title string) string {
	for _, input := range io.inputs {
		if input.Title == title {
			return input.Name
		}
	}
	// fallback to original if not found
	return title
}

// user friendly titles for the survey options
func NewInputOptions() *InputOptions {
	return &InputOptions{
		inputs: []InputOption{
			{"aws-cloudwatch", "AWS CloudWatch"},
			{"aws-s3", "AWS S3"},
			{"azure-blob-storage", "Azure Blob Storage"},
			{"azure-eventhub", "Azure Event Hub"},
			{"cel", "Common Event Language"},
			{"entity-analytics", "Entity Analytics"},
			{"etw", "Event Tracing for Windows"},
			{"filestream", "File Stream"},
			{"gcp-pubsub", "GCP Pub/Sub"},
			{"gcs", "Google Cloud Storage"},
			{"http_endpoint", "HTTP Endpoint"},
			{"httpjson", "HTTP JSON (Deprecated)"},
			{"journald", "Journald"},
			{"netflow", "NetFlow"},
			{"redis", "Redis"},
			{"tcp", "TCP"},
			{"udp", "UDP"},
			{"winlog", "Windows Event Log"},
		},
	}
}
