package utils

func RemoveItem[S ~[]E, E comparable](s S, values ...E) S {
	result := make(S, 0, len(s))
outer:
	for _, item := range s {
		for _, v := range values {
			if item == v {
				continue outer
			}
		}
		result = append(result, item)
	}
	return result
}

func Contains(slice []string, value string) bool {
	for _, item := range slice {
		if item == value {
			return true
		}
	}
	return false
}

func Mask(text string) string {
	res := ""
	if len(text) > 12 {
		res = text[:8] + "****" + text[len(text)-4:]
	} else if len(text) > 8 {
		res = text[:4] + "****" + text[len(text)-2:]
	} else {
		res = "****"
	}
	return res
}
