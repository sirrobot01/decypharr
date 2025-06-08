package types

type Error struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

func (e *Error) Error() string {
	return e.Message
}

var NoActiveAccountsError = &Error{
	Message: "No active accounts",
	Code:    "no_active_accounts",
}

var NoDownloadLinkError = &Error{
	Message: "No download link found",
	Code:    "no_download_link",
}

var DownloadLinkExpiredError = &Error{
	Message: "Download link expired",
	Code:    "download_link_expired",
}

var EmptyDownloadLinkError = &Error{
	Message: "Download link is empty",
	Code:    "empty_download_link",
}
