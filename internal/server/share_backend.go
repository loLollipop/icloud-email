package server

import (
	"errors"
	"net/http"

	"icloud-hme/internal/mail"
)

// SharingBackend is optional: existing administrative backends retain their API.
type SharingBackend interface {
	CaptureShareBoundary(string) (mail.ShareBoundary, error)
	ValidateShareBoundary(string, mail.ShareBoundary) error
	ValidateShareConfiguration(string, mail.ShareBoundary) error
	WithShareConfiguration(string, mail.ShareBoundary, func() error) error
	SearchShared(string, mail.ShareBoundary, string, int, int, string) (mail.SearchResult, error)
	GetSharedFull(string, mail.ShareBoundary, string, uint32) (*mail.FullMessage, error)
}

func (b *managerBackend) ValidateShareConfiguration(accountID string, boundary mail.ShareBoundary) error {
	return b.WithShareConfiguration(accountID, boundary, nil)
}

func (b *managerBackend) WithShareConfiguration(accountID string, boundary mail.ShareBoundary, commit func() error) error {
	err := b.mgr.WithMailboxConfiguration(accountID, boundary.ConfigurationVersion, commit)
	if errors.Is(err, mail.ErrShareIdentityChanged) {
		return shareUnavailableError()
	}
	return err
}

func (b *managerBackend) ValidateShareBoundary(accountID string, boundary mail.ShareBoundary) error {
	err := b.mgr.WithMailClient(accountID, func(mc *mail.Client) error {
		acc, exists := b.mgr.GetAccount(accountID)
		if !exists || acc.MailboxVersion != boundary.ConfigurationVersion {
			return mail.ErrShareIdentityChanged
		}
		return mc.ValidateShareBoundary(boundary)
	})
	return sharedBackendError(err)
}

func (b *managerBackend) CaptureShareBoundary(accountID string) (mail.ShareBoundary, error) {
	var boundary mail.ShareBoundary
	err := b.mgr.WithMailClient(accountID, func(mc *mail.Client) error {
		var err error
		boundary, err = mc.CaptureShareBoundary()
		if err == nil {
			acc, exists := b.mgr.GetAccount(accountID)
			if !exists {
				return mail.ErrShareIdentityChanged
			}
			boundary.ConfigurationVersion = acc.MailboxVersion
		}
		return err
	})
	return boundary, sharedBackendError(err)
}

func (b *managerBackend) SearchShared(accountID string, boundary mail.ShareBoundary, email string, page, pageSize int, query string) (mail.SearchResult, error) {
	var result mail.SearchResult
	err := b.mgr.WithMailClient(accountID, func(mc *mail.Client) error {
		acc, exists := b.mgr.GetAccount(accountID)
		if !exists || acc.MailboxVersion != boundary.ConfigurationVersion {
			return mail.ErrShareIdentityChanged
		}
		var err error
		result, err = mc.SearchShared(boundary, email, page, pageSize, query)
		return err
	})
	return result, sharedBackendError(err)
}

func (b *managerBackend) GetSharedFull(accountID string, boundary mail.ShareBoundary, email string, uid uint32) (*mail.FullMessage, error) {
	var result *mail.FullMessage
	err := b.mgr.WithMailClient(accountID, func(mc *mail.Client) error {
		acc, exists := b.mgr.GetAccount(accountID)
		if !exists || acc.MailboxVersion != boundary.ConfigurationVersion {
			return mail.ErrShareIdentityChanged
		}
		var err error
		result, err = mc.GetSharedFull(boundary, email, uid)
		return err
	})
	return result, sharedBackendError(err)
}

func sharedBackendError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, mail.ErrShareIdentityChanged) {
		return shareUnavailableError()
	}
	if errors.Is(err, mail.ErrMessageOutsideHME) {
		return messageNotFoundError()
	}
	return sharedUpstreamError()
}

func sharedUpstreamError() *BackendError {
	return &BackendError{Status: http.StatusServiceUnavailable, Code: "UPSTREAM_FAILURE", Message: "共享邮件暂不可用"}
}

func shareUnavailableError() *BackendError {
	return &BackendError{Status: http.StatusNotFound, Code: "SHARE_UNAVAILABLE", Message: "分享链接不可用"}
}
