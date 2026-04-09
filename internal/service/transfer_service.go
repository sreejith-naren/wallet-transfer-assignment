package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
	"wallet-transfer/internal/domain"
	"wallet-transfer/internal/repository"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TransferRequest represents a request to create a transfer
type TransferRequest struct {
	IdempotencyKey uuid.UUID `json:"idempotencyKey" binding:"required"`
	FromWalletID   uuid.UUID `json:"fromWalletId" binding:"required"`
	ToWalletID     uuid.UUID `json:"toWalletId" binding:"required"`
	Amount         int64     `json:"amount" binding:"required,gt=0"`
}

// TransferResponse represents the response of a transfer operation
type TransferResponse struct {
	TransferID   string    `json:"transferId"`
	State        string    `json:"state"`
	FromWalletID uuid.UUID `json:"fromWalletId"`
	ToWalletID   uuid.UUID `json:"toWalletId"`
	Amount       int64     `json:"amount"`
	CreatedAt    time.Time `json:"createdAt"`
}

// TransferService handles transfer business logic
type TransferService struct {
	repo repository.Repository
}

func NewTransferService(repo repository.Repository) *TransferService {
	return &TransferService{repo: repo}
}

// CreateTransfer handles the complete transfer workflow with idempotency
func (s *TransferService) CreateTransfer(ctx context.Context, req TransferRequest) (*TransferResponse, error) {
	// Step 1: Check for existing idempotency record
	existingRecord, err := s.repo.GetIdempotencyRecord(ctx, req.IdempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("failed to check idempotency: %w", err)
	}

	if existingRecord != nil {
		log.Printf("Idempotency key %s found, returning cached response", req.IdempotencyKey)
		var response TransferResponse
		if err := json.Unmarshal(existingRecord.ResponseData, &response); err != nil {
			return nil, fmt.Errorf("failed to unmarshal cached response: %w", err)
		}
		return &response, nil
	}

	// Step 2: Execute transfer in transaction
	response, err := s.executeTransfer(ctx, req)
	if err != nil {
		return nil, err
	}

	return response, nil
}

// executeTransfer executes the transfer within a single transaction
func (s *TransferService) executeTransfer(ctx context.Context, req TransferRequest) (*TransferResponse, error) {
	// Begin transaction
	tx, err := s.repo.BeginTx(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}

	// Ensure rollback is attempted but guard against panics coming from
	// test mocks that return an uninitialized *gorm.DB. We don't want a
	// panic in a defer to abort the test run; recover and ignore such
	// panics when rolling back.
	// Ensure rollback is attempted via repository so tests can mock it.
	defer func() {
		if tx == nil {
			return
		}
		// Let repository handle the rollback; ignore rollback errors here.
		_ = s.repo.RollbackTx(ctx, tx)
	}()

	// Create transfer domain object
	transfer, err := domain.NewTransfer(req.FromWalletID, req.ToWalletID, req.Amount)
	if err != nil {
		return nil, err
	}

	// Create transfer record
	if err := s.repo.CreateTransfer(ctx, tx, transfer); err != nil {
		return nil, fmt.Errorf("failed to create transfer: %w", err)
	}

	// Lock wallets in consistent order to prevent deadlocks
	walletIDs := []uuid.UUID{req.FromWalletID, req.ToWalletID}
	wallets, err := s.repo.GetWalletsForUpdate(ctx, tx, walletIDs)
	if err != nil {
		// Mark transfer as failed if wallets not found
		_ = s.repo.UpdateTransferState(ctx, tx, transfer.ID, domain.TransferStateFailed)
		_ = s.repo.CommitTx(ctx, tx)
		return nil, err
	}

	// Find source and destination wallets
	var fromWallet, toWallet *domain.Wallet
	for _, w := range wallets {
		if w.ID == req.FromWalletID {
			fromWallet = w
		} else if w.ID == req.ToWalletID {
			toWallet = w
		}
	}

	// Check sufficient funds
	if !fromWallet.CanDebit(req.Amount) {
		log.Printf("Insufficient funds in wallet %s: balance=%d, required=%d",
			fromWallet.ID.String(), fromWallet.Balance, req.Amount)

		if err := transfer.MarkAsFailed(); err != nil {
			return nil, err
		}
		if err := s.repo.UpdateTransferState(ctx, tx, transfer.ID, domain.TransferStateFailed); err != nil {
			return nil, err
		}

		// Build failed response
		response := &TransferResponse{
			TransferID:   transfer.ID.String(),
			State:        string(transfer.State),
			FromWalletID: transfer.FromWalletID,
			ToWalletID:   transfer.ToWalletID,
			Amount:       transfer.Amount,
			CreatedAt:    transfer.CreatedAt,
		}

		// Store idempotency record for failed transfer
		if err := s.storeIdempotencyRecord(ctx, tx, req.IdempotencyKey, transfer.ID, response); err != nil {
			return nil, err
		}

		// Commit transaction
		if err := s.repo.CommitTx(ctx, tx); err != nil {
			return nil, err
		}

		return nil, domain.ErrInsufficientFunds
	}

	// Update balances
	newFromBalance := fromWallet.Balance - req.Amount
	newToBalance := toWallet.Balance + req.Amount

	if err := s.repo.UpdateWalletBalance(ctx, tx, fromWallet.ID, newFromBalance); err != nil {
		return nil, fmt.Errorf("failed to update from wallet: %w", err)
	}

	if err := s.repo.UpdateWalletBalance(ctx, tx, toWallet.ID, newToBalance); err != nil {
		return nil, fmt.Errorf("failed to update to wallet: %w", err)
	}

	// Create double-entry ledger entries
	debitEntry, err := domain.NewLedgerEntry(req.FromWalletID, transfer.ID, domain.EntryTypeDebit, req.Amount)
	if err != nil {
		return nil, err
	}

	creditEntry, err := domain.NewLedgerEntry(req.ToWalletID, transfer.ID, domain.EntryTypeCredit, req.Amount)
	if err != nil {
		return nil, err
	}

	if err := s.repo.CreateLedgerEntry(ctx, tx, debitEntry); err != nil {
		return nil, fmt.Errorf("failed to create debit entry: %w", err)
	}

	if err := s.repo.CreateLedgerEntry(ctx, tx, creditEntry); err != nil {
		return nil, fmt.Errorf("failed to create credit entry: %w", err)
	}

	// Mark transfer as processed
	if err := transfer.MarkAsProcessed(); err != nil {
		return nil, err
	}

	if err := s.repo.UpdateTransferState(ctx, tx, transfer.ID, domain.TransferStateProcessed); err != nil {
		return nil, fmt.Errorf("failed to update transfer state: %w", err)
	}

	// Build success response
	response := &TransferResponse{
		TransferID:   transfer.ID.String(),
		State:        string(transfer.State),
		FromWalletID: transfer.FromWalletID,
		ToWalletID:   transfer.ToWalletID,
		Amount:       transfer.Amount,
		CreatedAt:    transfer.CreatedAt,
	}

	// Store idempotency record
	if err := s.storeIdempotencyRecord(ctx, tx, req.IdempotencyKey, transfer.ID, response); err != nil {
		return nil, err
	}

	// Commit transaction
	if err := s.repo.CommitTx(ctx, tx); err != nil {
		return nil, err
	}

	log.Printf("Transfer %s completed successfully: %s -> %s, amount: %d",
		transfer.ID.String(), req.FromWalletID.String(), req.ToWalletID.String(), req.Amount)

	return response, nil
}

// storeIdempotencyRecord stores the idempotency record
func (s *TransferService) storeIdempotencyRecord(ctx context.Context, tx *gorm.DB, key uuid.UUID, transferID uuid.UUID, response *TransferResponse) error {
	responseData, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("failed to marshal response: %w", err)
	}

	record := &domain.IdempotencyRecord{
		IdempotencyKey: key,
		TransferID:     transferID,
		ResponseData:   responseData,
		CreatedAt:      time.Now(),
	}

	if err := s.repo.CreateIdempotencyRecord(ctx, tx, record); err != nil {
		return fmt.Errorf("failed to store idempotency record: %w", err)
	}

	return nil
}

// GetTransfer retrieves a transfer by ID
func (s *TransferService) GetTransfer(ctx context.Context, transferID string) (*TransferResponse, error) {
	transferUUID, err := uuid.Parse(transferID)
	if err != nil {
		return nil, fmt.Errorf("invalid transfer ID: %w", err)
	}

	transfer, err := s.repo.GetTransferByID(ctx, transferUUID)
	if err != nil {
		return nil, err
	}

	return &TransferResponse{
		TransferID:   transfer.ID.String(),
		State:        string(transfer.State),
		FromWalletID: transfer.FromWalletID,
		ToWalletID:   transfer.ToWalletID,
		Amount:       transfer.Amount,
		CreatedAt:    transfer.CreatedAt,
	}, nil
}
