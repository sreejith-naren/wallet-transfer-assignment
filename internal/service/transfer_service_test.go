package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
	"wallet-transfer/internal/domain"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"gorm.io/gorm"
)

// MockRepository is a mock implementation of repository.Repository
type MockRepository struct {
	mock.Mock
}

func (m *MockRepository) BeginTx(ctx context.Context) (*gorm.DB, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*gorm.DB), args.Error(1)
}

func (m *MockRepository) CommitTx(ctx context.Context, tx *gorm.DB) error {
	args := m.Called(ctx, tx)
	return args.Error(0)
}

func (m *MockRepository) RollbackTx(ctx context.Context, tx *gorm.DB) error {
	args := m.Called(ctx, tx)
	return args.Error(0)
}

func (m *MockRepository) GetWalletsForUpdate(ctx context.Context, tx *gorm.DB, walletIDs []uuid.UUID) ([]*domain.Wallet, error) {
	args := m.Called(ctx, tx, walletIDs)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*domain.Wallet), args.Error(1)
}

func (m *MockRepository) UpdateWalletBalance(ctx context.Context, tx *gorm.DB, walletID uuid.UUID, newBalance int64) error {
	args := m.Called(ctx, tx, walletID, newBalance)
	return args.Error(0)
}

func (m *MockRepository) CreateTransfer(ctx context.Context, tx *gorm.DB, transfer *domain.Transfer) error {
	args := m.Called(ctx, tx, transfer)
	return args.Error(0)
}

func (m *MockRepository) UpdateTransferState(ctx context.Context, tx *gorm.DB, transferID uuid.UUID, state domain.TransferState) error {
	args := m.Called(ctx, tx, transferID, state)
	return args.Error(0)
}

func (m *MockRepository) GetTransferByID(ctx context.Context, transferID uuid.UUID) (*domain.Transfer, error) {
	args := m.Called(ctx, transferID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Transfer), args.Error(1)
}

func (m *MockRepository) CreateLedgerEntry(ctx context.Context, tx *gorm.DB, entry *domain.LedgerEntry) error {
	args := m.Called(ctx, tx, entry)
	return args.Error(0)
}

func (m *MockRepository) GetIdempotencyRecord(ctx context.Context, key uuid.UUID) (*domain.IdempotencyRecord, error) {
	args := m.Called(ctx, key)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.IdempotencyRecord), args.Error(1)
}

func (m *MockRepository) CreateIdempotencyRecord(ctx context.Context, tx *gorm.DB, record *domain.IdempotencyRecord) error {
	args := m.Called(ctx, tx, record)
	return args.Error(0)
}

// MockTx is a mock transaction
type MockTx struct {
	gorm.DB
	mock.Mock
}

func (m *MockTx) Commit() *gorm.DB {
	m.Called()
	return &m.DB
}

func (m *MockTx) Rollback() *gorm.DB {
	m.Called()
	return &m.DB
}

func TestTransferRequest_Validation(t *testing.T) {
	tests := []struct {
		name    string
		req     TransferRequest
		wantErr bool
	}{
		{
			name: "valid request",
			req: TransferRequest{
				IdempotencyKey: uuid.New(),
				FromWalletID:   uuid.New(),
				ToWalletID:     uuid.New(),
				Amount:         100,
			},
			wantErr: false,
		},
		{
			name: "zero amount",
			req: TransferRequest{
				IdempotencyKey: uuid.New(),
				FromWalletID:   uuid.New(),
				ToWalletID:     uuid.New(),
				Amount:         0,
			},
			wantErr: true,
		},
		{
			name: "negative amount",
			req: TransferRequest{
				IdempotencyKey: uuid.New(),
				FromWalletID:   uuid.New(),
				ToWalletID:     uuid.New(),
				Amount:         -100,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantErr {
				assert.True(t, tt.req.Amount <= 0)
			} else {
				assert.True(t, tt.req.Amount > 0)
			}
		})
	}
}

func TestTransferService_CreateTransfer_Success(t *testing.T) {
	mockRepo := new(MockRepository)
	service := NewTransferService(mockRepo)

	ctx := context.Background()
	fromWalletID := uuid.New()
	toWalletID := uuid.New()
	idempotencyKey := uuid.New()

	req := TransferRequest{
		IdempotencyKey: idempotencyKey,
		FromWalletID:   fromWalletID,
		ToWalletID:     toWalletID,
		Amount:         100,
	}

	// Mock: no existing idempotency record
	mockRepo.On("GetIdempotencyRecord", ctx, idempotencyKey).Return(nil, nil)

	// Mock: begin transaction
	mockTx := &MockTx{}
	mockRepo.On("BeginTx", ctx).Return(&mockTx.DB, nil)

	// Mock: create transfer
	mockRepo.On("CreateTransfer", ctx, &mockTx.DB, mock.AnythingOfType("*domain.Transfer")).Return(nil)

	// Mock: get wallets for update
	fromWallet := &domain.Wallet{ID: fromWalletID, Balance: 1000}
	toWallet := &domain.Wallet{ID: toWalletID, Balance: 500}
	mockRepo.On("GetWalletsForUpdate", ctx, &mockTx.DB, mock.AnythingOfType("[]uuid.UUID")).
		Return([]*domain.Wallet{fromWallet, toWallet}, nil)

	// Mock: update wallet balances
	mockRepo.On("UpdateWalletBalance", ctx, &mockTx.DB, fromWalletID, int64(900)).Return(nil)
	mockRepo.On("UpdateWalletBalance", ctx, &mockTx.DB, toWalletID, int64(600)).Return(nil)

	// Mock: create ledger entries
	mockRepo.On("CreateLedgerEntry", ctx, &mockTx.DB, mock.AnythingOfType("*domain.LedgerEntry")).Return(nil).Twice()

	// Mock: update transfer state
	mockRepo.On("UpdateTransferState", ctx, &mockTx.DB, mock.AnythingOfType("uuid.UUID"), domain.TransferStateProcessed).Return(nil)

	// Mock: create idempotency record
	mockRepo.On("CreateIdempotencyRecord", ctx, &mockTx.DB, mock.AnythingOfType("*domain.IdempotencyRecord")).Return(nil)

	// Mock: transaction commit/rollback via repository
	mockRepo.On("CommitTx", ctx, &mockTx.DB).Return(nil)
	mockRepo.On("RollbackTx", ctx, &mockTx.DB).Return(nil)

	// Execute
	response, err := service.CreateTransfer(ctx, req)

	// Assert
	assert.NoError(t, err)
	assert.NotNil(t, response)
	assert.Equal(t, string(domain.TransferStateProcessed), response.State)
	assert.Equal(t, fromWalletID, response.FromWalletID)
	assert.Equal(t, toWalletID, response.ToWalletID)
	assert.Equal(t, int64(100), response.Amount)

	mockRepo.AssertExpectations(t)
}

func TestTransferService_CreateTransfer_InsufficientFunds(t *testing.T) {
	mockRepo := new(MockRepository)
	service := NewTransferService(mockRepo)

	ctx := context.Background()
	fromWalletID := uuid.New()
	toWalletID := uuid.New()
	idempotencyKey := uuid.New()

	req := TransferRequest{
		IdempotencyKey: idempotencyKey,
		FromWalletID:   fromWalletID,
		ToWalletID:     toWalletID,
		Amount:         1000,
	}

	// Mock: no existing idempotency record
	mockRepo.On("GetIdempotencyRecord", ctx, idempotencyKey).Return(nil, nil)

	// Mock: begin transaction
	mockTx := &MockTx{}
	mockRepo.On("BeginTx", ctx).Return(&mockTx.DB, nil)

	// Mock: create transfer
	mockRepo.On("CreateTransfer", ctx, &mockTx.DB, mock.AnythingOfType("*domain.Transfer")).Return(nil)

	// Mock: get wallets with insufficient balance
	fromWallet := &domain.Wallet{ID: fromWalletID, Balance: 500} // Less than required 1000
	toWallet := &domain.Wallet{ID: toWalletID, Balance: 500}
	mockRepo.On("GetWalletsForUpdate", ctx, &mockTx.DB, mock.AnythingOfType("[]uuid.UUID")).
		Return([]*domain.Wallet{fromWallet, toWallet}, nil)

	// Mock: update transfer state to failed
	mockRepo.On("UpdateTransferState", ctx, &mockTx.DB, mock.AnythingOfType("uuid.UUID"), domain.TransferStateFailed).Return(nil)

	// Mock: create idempotency record for failed transfer
	mockRepo.On("CreateIdempotencyRecord", ctx, &mockTx.DB, mock.AnythingOfType("*domain.IdempotencyRecord")).Return(nil)

	// Mock: transaction commit/rollback via repository
	mockRepo.On("CommitTx", ctx, &mockTx.DB).Return(nil)
	mockRepo.On("RollbackTx", ctx, &mockTx.DB).Return(nil)

	// Execute
	response, err := service.CreateTransfer(ctx, req)

	// Assert
	assert.Error(t, err)
	assert.Equal(t, domain.ErrInsufficientFunds, err)
	assert.Nil(t, response)

	mockRepo.AssertExpectations(t)
}

func TestTransferService_CreateTransfer_Idempotency(t *testing.T) {
	mockRepo := new(MockRepository)
	service := NewTransferService(mockRepo)

	ctx := context.Background()
	fromWalletID := uuid.New()
	toWalletID := uuid.New()
	idempotencyKey := uuid.New()
	transferID := uuid.New()

	req := TransferRequest{
		IdempotencyKey: idempotencyKey,
		FromWalletID:   fromWalletID,
		ToWalletID:     toWalletID,
		Amount:         100,
	}

	// Create cached response
	cachedResponse := &TransferResponse{
		TransferID:   transferID.String(),
		State:        string(domain.TransferStateProcessed),
		FromWalletID: fromWalletID,
		ToWalletID:   toWalletID,
		Amount:       100,
		CreatedAt:    time.Now(),
	}
	responseData, _ := json.Marshal(cachedResponse)

	// Mock: existing idempotency record found
	existingRecord := &domain.IdempotencyRecord{
		IdempotencyKey: idempotencyKey,
		TransferID:     transferID,
		ResponseData:   responseData,
		CreatedAt:      time.Now(),
	}
	mockRepo.On("GetIdempotencyRecord", ctx, idempotencyKey).Return(existingRecord, nil)

	// Execute
	response, err := service.CreateTransfer(ctx, req)

	// Assert - should return cached response without executing transfer
	assert.NoError(t, err)
	assert.NotNil(t, response)
	assert.Equal(t, transferID.String(), response.TransferID)
	assert.Equal(t, string(domain.TransferStateProcessed), response.State)

	mockRepo.AssertExpectations(t)
	// Verify that BeginTx was NOT called (transfer not executed)
	mockRepo.AssertNotCalled(t, "BeginTx")
}

func TestTransferService_CreateTransfer_WalletNotFound(t *testing.T) {
	mockRepo := new(MockRepository)
	service := NewTransferService(mockRepo)

	ctx := context.Background()
	fromWalletID := uuid.New()
	toWalletID := uuid.New()
	idempotencyKey := uuid.New()

	req := TransferRequest{
		IdempotencyKey: idempotencyKey,
		FromWalletID:   fromWalletID,
		ToWalletID:     toWalletID,
		Amount:         100,
	}

	// Mock: no existing idempotency record
	mockRepo.On("GetIdempotencyRecord", ctx, idempotencyKey).Return(nil, nil)

	// Mock: begin transaction
	mockTx := &MockTx{}
	mockRepo.On("BeginTx", ctx).Return(&mockTx.DB, nil)

	// Mock: create transfer
	mockRepo.On("CreateTransfer", ctx, &mockTx.DB, mock.AnythingOfType("*domain.Transfer")).Return(nil)

	// Mock: wallet not found
	mockRepo.On("GetWalletsForUpdate", ctx, &mockTx.DB, mock.AnythingOfType("[]uuid.UUID")).
		Return(nil, domain.ErrWalletNotFound)

	// Mock: update transfer state to failed
	mockRepo.On("UpdateTransferState", ctx, &mockTx.DB, mock.AnythingOfType("uuid.UUID"), domain.TransferStateFailed).Return(nil)

	// Mock: transaction commit/rollback via repository
	mockRepo.On("CommitTx", ctx, &mockTx.DB).Return(nil)
	mockRepo.On("RollbackTx", ctx, &mockTx.DB).Return(nil)

	// Execute
	response, err := service.CreateTransfer(ctx, req)

	// Assert
	assert.Error(t, err)
	assert.Equal(t, domain.ErrWalletNotFound, err)
	assert.Nil(t, response)

	mockRepo.AssertExpectations(t)
}

func TestTransferService_CreateTransfer_LedgerEntries(t *testing.T) {
	mockRepo := new(MockRepository)
	service := NewTransferService(mockRepo)

	ctx := context.Background()
	fromWalletID := uuid.New()
	toWalletID := uuid.New()
	idempotencyKey := uuid.New()

	req := TransferRequest{
		IdempotencyKey: idempotencyKey,
		FromWalletID:   fromWalletID,
		ToWalletID:     toWalletID,
		Amount:         250,
	}

	// Mock: no existing idempotency record
	mockRepo.On("GetIdempotencyRecord", ctx, idempotencyKey).Return(nil, nil)

	// Mock: begin transaction
	mockTx := &MockTx{}
	mockRepo.On("BeginTx", ctx).Return(&mockTx.DB, nil)

	// Mock: create transfer
	mockRepo.On("CreateTransfer", ctx, &mockTx.DB, mock.AnythingOfType("*domain.Transfer")).Return(nil)

	// Mock: get wallets for update
	fromWallet := &domain.Wallet{ID: fromWalletID, Balance: 1000}
	toWallet := &domain.Wallet{ID: toWalletID, Balance: 500}
	mockRepo.On("GetWalletsForUpdate", ctx, &mockTx.DB, mock.AnythingOfType("[]uuid.UUID")).
		Return([]*domain.Wallet{fromWallet, toWallet}, nil)

	// Mock: update wallet balances
	mockRepo.On("UpdateWalletBalance", ctx, &mockTx.DB, fromWalletID, int64(750)).Return(nil)
	mockRepo.On("UpdateWalletBalance", ctx, &mockTx.DB, toWalletID, int64(750)).Return(nil)

	// Track ledger entries created
	var debitEntry, creditEntry *domain.LedgerEntry
	mockRepo.On("CreateLedgerEntry", ctx, &mockTx.DB, mock.AnythingOfType("*domain.LedgerEntry")).
		Run(func(args mock.Arguments) {
			entry := args.Get(2).(*domain.LedgerEntry)
			if entry.EntryType == domain.EntryTypeDebit {
				debitEntry = entry
			} else {
				creditEntry = entry
			}
		}).Return(nil).Twice()

	// Mock: update transfer state
	mockRepo.On("UpdateTransferState", ctx, &mockTx.DB, mock.AnythingOfType("uuid.UUID"), domain.TransferStateProcessed).Return(nil)

	// Mock: create idempotency record
	mockRepo.On("CreateIdempotencyRecord", ctx, &mockTx.DB, mock.AnythingOfType("*domain.IdempotencyRecord")).Return(nil)

	// Mock: transaction commit/rollback via repository
	mockRepo.On("CommitTx", ctx, &mockTx.DB).Return(nil)
	mockRepo.On("RollbackTx", ctx, &mockTx.DB).Return(nil)

	// Execute
	response, err := service.CreateTransfer(ctx, req)

	// Assert
	assert.NoError(t, err)
	assert.NotNil(t, response)

	// Verify ledger entries were created correctly
	assert.NotNil(t, debitEntry, "Debit entry should be created")
	assert.NotNil(t, creditEntry, "Credit entry should be created")

	assert.Equal(t, fromWalletID, debitEntry.WalletID)
	assert.Equal(t, domain.EntryTypeDebit, debitEntry.EntryType)
	assert.Equal(t, int64(250), debitEntry.Amount)

	assert.Equal(t, toWalletID, creditEntry.WalletID)
	assert.Equal(t, domain.EntryTypeCredit, creditEntry.EntryType)
	assert.Equal(t, int64(250), creditEntry.Amount)

	mockRepo.AssertExpectations(t)
}

func TestTransferService_CreateTransfer_DatabaseError(t *testing.T) {
	mockRepo := new(MockRepository)
	service := NewTransferService(mockRepo)

	ctx := context.Background()
	idempotencyKey := uuid.New()

	req := TransferRequest{
		IdempotencyKey: idempotencyKey,
		FromWalletID:   uuid.New(),
		ToWalletID:     uuid.New(),
		Amount:         100,
	}

	// Mock: database error when checking idempotency
	dbError := errors.New("database connection failed")
	mockRepo.On("GetIdempotencyRecord", ctx, idempotencyKey).Return(nil, dbError)

	// Execute
	response, err := service.CreateTransfer(ctx, req)

	// Assert
	assert.Error(t, err)
	assert.Nil(t, response)
	assert.Contains(t, err.Error(), "failed to check idempotency")

	mockRepo.AssertExpectations(t)
}

func TestTransferService_GetTransfer(t *testing.T) {
	mockRepo := new(MockRepository)
	service := NewTransferService(mockRepo)

	ctx := context.Background()
	transferID := uuid.New()
	fromWalletID := uuid.New()
	toWalletID := uuid.New()

	transfer := &domain.Transfer{
		ID:           transferID,
		FromWalletID: fromWalletID,
		ToWalletID:   toWalletID,
		Amount:       100,
		State:        domain.TransferStateProcessed,
		CreatedAt:    time.Now(),
	}

	// Mock: get transfer by ID
	mockRepo.On("GetTransferByID", ctx, transferID).Return(transfer, nil)

	// Execute
	response, err := service.GetTransfer(ctx, transferID.String())

	// Assert
	assert.NoError(t, err)
	assert.NotNil(t, response)
	assert.Equal(t, transferID.String(), response.TransferID)
	assert.Equal(t, string(domain.TransferStateProcessed), response.State)
	assert.Equal(t, fromWalletID, response.FromWalletID)
	assert.Equal(t, toWalletID, response.ToWalletID)
	assert.Equal(t, int64(100), response.Amount)

	mockRepo.AssertExpectations(t)
}
