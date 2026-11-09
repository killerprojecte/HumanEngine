//go:build web_read_candidate

package webread

func runtimeInvoke(input []byte, operation string) any {
	switch operation {
	case "toolValidate":
		return toolValidation(input)
	case "configurationValidate":
		return configuration(input)
	case "networkPrepare", "networkChoose", "networkHeaders", "networkRedirect":
		return networkInvoke(input, operation)
	}
	return serviceInvoke(input, operation)
}
