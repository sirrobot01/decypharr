package utils

import "slices"

func RemoveItem[S ~[]E, E comparable](s S, values ...E) S {
	result := make(S, 0, len(s))
	for _, item := range s {
		if !slices.Contains(values, item) {
			result = append(result, item)
		}
	}
	return result
}

func Mask(text string) string {
	switch {
	case len(text) > 12:
		return text[:8] + "****" + text[len(text)-4:]
	case len(text) > 8:
		return text[:4] + "****" + text[len(text)-2:]
	default:
		return "****"
	}
}
