package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/hcnet/go/clients/auroraclient"
	"github.com/hcnet/go/historyarchive"
	"github.com/hcnet/go/keypair"
	auroracmd "github.com/hcnet/go/services/aurora/cmd"
	aurora "github.com/hcnet/go/services/aurora/internal"
	"github.com/hcnet/go/services/aurora/internal/db2/history"
	"github.com/hcnet/go/services/aurora/internal/db2/schema"
	"github.com/hcnet/go/services/aurora/internal/test/integration"
	"github.com/hcnet/go/support/collections/set"
	"github.com/hcnet/go/support/db"
	"github.com/hcnet/go/support/db/dbtest"
	"github.com/hcnet/go/txnbuild"
	"github.com/hcnet/go/xdr"
)

func submitLiquidityPoolOps(itest *integration.Test, tt *assert.Assertions) (submittedOperations []txnbuild.Operation, lastLedger int32) {
	master := itest.Master()
	keys, accounts := itest.CreateAccounts(2, "1000")
	shareKeys, shareAccount := keys[0], accounts[0]
	tradeKeys, tradeAccount := keys[1], accounts[1]

	allOps := []txnbuild.Operation{
		&txnbuild.ChangeTrust{
			Line: txnbuild.ChangeTrustAssetWrapper{
				Asset: txnbuild.CreditAsset{
					Code:   "USD",
					Issuer: master.Address(),
				},
			},
			Limit: txnbuild.MaxTrustlineLimit,
		},
		&txnbuild.ChangeTrust{
			Line: txnbuild.LiquidityPoolShareChangeTrustAsset{
				LiquidityPoolParameters: txnbuild.LiquidityPoolParameters{
					AssetA: txnbuild.NativeAsset{},
					AssetB: txnbuild.CreditAsset{
						Code:   "USD",
						Issuer: master.Address(),
					},
					Fee: 30,
				},
			},
			Limit: txnbuild.MaxTrustlineLimit,
		},
		&txnbuild.Payment{
			SourceAccount: master.Address(),
			Destination:   shareAccount.GetAccountID(),
			Asset: txnbuild.CreditAsset{
				Code:   "USD",
				Issuer: master.Address(),
			},
			Amount: "1000",
		},
	}
	itest.MustSubmitMultiSigOperations(shareAccount, []*keypair.Full{shareKeys, master}, allOps...)

	poolID, err := xdr.NewPoolId(
		xdr.MustNewNativeAsset(),
		xdr.MustNewCreditAsset("USD", master.Address()),
		30,
	)
	tt.NoError(err)
	poolIDHexString := xdr.Hash(poolID).HexString()

	var op txnbuild.Operation = &txnbuild.LiquidityPoolDeposit{
		LiquidityPoolID: [32]byte(poolID),
		MaxAmountA:      "400",
		MaxAmountB:      "777",
		MinPrice:        xdr.Price{1, 2},
		MaxPrice:        xdr.Price{2, 1},
	}
	allOps = append(allOps, op)
	itest.MustSubmitOperations(shareAccount, shareKeys, op)

	ops := []txnbuild.Operation{
		&txnbuild.ChangeTrust{
			Line: txnbuild.ChangeTrustAssetWrapper{
				Asset: txnbuild.CreditAsset{
					Code:   "USD",
					Issuer: master.Address(),
				},
			},
			Limit: txnbuild.MaxTrustlineLimit,
		},
		&txnbuild.PathPaymentStrictReceive{
			SendAsset: txnbuild.NativeAsset{},
			DestAsset: txnbuild.CreditAsset{
				Code:   "USD",
				Issuer: master.Address(),
			},
			SendMax:     "1000",
			DestAmount:  "2",
			Destination: tradeKeys.Address(),
		},
	}
	itest.MustSubmitOperations(tradeAccount, tradeKeys, ops...)

	pool, err := itest.Client().LiquidityPoolDetail(auroraclient.LiquidityPoolRequest{
		LiquidityPoolID: poolIDHexString,
	})
	tt.NoError(err)

	op = &txnbuild.LiquidityPoolWithdraw{
		LiquidityPoolID: [32]byte(poolID),
		Amount:          pool.TotalShares,
		MinAmountA:      "10",
		MinAmountB:      "20",
	}
	allOps = append(allOps, op)
	txResp := itest.MustSubmitOperations(shareAccount, shareKeys, op)

	return allOps, txResp.Ledger
}

func submitPaymentOps(itest *integration.Test, tt *assert.Assertions) (submittedOperations []txnbuild.Operation, lastLedger int32) {
	ops := []txnbuild.Operation{
		&txnbuild.Payment{
			Destination: itest.Master().Address(),
			Amount:      "10",
			Asset:       txnbuild.NativeAsset{},
		},
		&txnbuild.PathPaymentStrictSend{
			SendAsset:   txnbuild.NativeAsset{},
			SendAmount:  "10",
			Destination: itest.Master().Address(),
			DestAsset:   txnbuild.NativeAsset{},
			DestMin:     "10",
			Path:        []txnbuild.Asset{txnbuild.NativeAsset{}},
		},
		&txnbuild.PathPaymentStrictReceive{
			SendAsset:   txnbuild.NativeAsset{},
			SendMax:     "10",
			Destination: itest.Master().Address(),
			DestAsset:   txnbuild.NativeAsset{},
			DestAmount:  "10",
			Path:        []txnbuild.Asset{txnbuild.NativeAsset{}},
		},
		&txnbuild.PathPayment{
			SendAsset:   txnbuild.NativeAsset{},
			SendMax:     "10",
			Destination: itest.Master().Address(),
			DestAsset:   txnbuild.NativeAsset{},
			DestAmount:  "10",
			Path:        []txnbuild.Asset{txnbuild.NativeAsset{}},
		},
	}
	txResp := itest.MustSubmitOperations(itest.MasterAccount(), itest.Master(), ops...)

	return ops, txResp.Ledger
}

func submitSponsorshipOps(itest *integration.Test, tt *assert.Assertions) (submittedOperations []txnbuild.Operation, lastLedger int32) {
	keys, accounts := itest.CreateAccounts(1, "1000")
	sponsor, sponsorPair := accounts[0], keys[0]
	newAccountKeys := keypair.MustRandom()
	newAccountID := newAccountKeys.Address()

	ops := sponsorOperations(newAccountID,
		&txnbuild.CreateAccount{
			Destination: newAccountID,
			Amount:      "100",
		})

	signers := []*keypair.Full{sponsorPair, newAccountKeys}
	allOps := ops
	itest.MustSubmitMultiSigOperations(sponsor, signers, ops...)

	// Revoke sponsorship
	op := &txnbuild.RevokeSponsorship{
		SponsorshipType: txnbuild.RevokeSponsorshipTypeAccount,
		Account:         &newAccountID,
	}
	allOps = append(allOps, op)
	txResp := itest.MustSubmitOperations(sponsor, sponsorPair, op)

	return allOps, txResp.Ledger
}

func submitClawbackOps(itest *integration.Test, tt *assert.Assertions) (submittedOperations []txnbuild.Operation, lastLedger int32) {
	// Give the master account the revocable flag (needed to set the clawback flag)
	setRevocableFlag := txnbuild.SetOptions{
		SetFlags: []txnbuild.AccountFlag{
			txnbuild.AuthRevocable,
		},
	}
	allOps := []txnbuild.Operation{&setRevocableFlag}

	itest.MustSubmitOperations(itest.MasterAccount(), itest.Master(), &setRevocableFlag)

	// Give the master account the clawback flag
	setClawBackFlag := txnbuild.SetOptions{
		SetFlags: []txnbuild.AccountFlag{
			txnbuild.AuthClawbackEnabled,
		},
	}
	allOps = append(allOps, &setClawBackFlag)
	itest.MustSubmitOperations(itest.MasterAccount(), itest.Master(), &setClawBackFlag)

	// Create another account from which to claw an asset back
	keyPairs, accounts := itest.CreateAccounts(1, "100")
	accountKeyPair := keyPairs[0]
	account := accounts[0]

	// Add some assets to the account with asset which allows clawback

	// Time machine to Spain before Euros were a thing
	pesetasAsset := txnbuild.CreditAsset{Code: "PTS", Issuer: itest.Master().Address()}
	itest.MustEstablishTrustline(accountKeyPair, account, pesetasAsset)
	pesetasPayment := txnbuild.Payment{
		Destination: accountKeyPair.Address(),
		Amount:      "10",
		Asset:       pesetasAsset,
	}
	allOps = append(allOps, &pesetasPayment)
	itest.MustSubmitOperations(itest.MasterAccount(), itest.Master(), &pesetasPayment)

	clawback := txnbuild.Clawback{
		From:   account.GetAccountID(),
		Amount: "10",
		Asset:  pesetasAsset,
	}
	allOps = append(allOps, &clawback)
	itest.MustSubmitOperations(itest.MasterAccount(), itest.Master(), &clawback)

	// Make a claimable balance from the master account (and asset issuer) to the account with an asset which allows clawback
	pesetasCreateCB := txnbuild.CreateClaimableBalance{
		Amount: "10",
		Asset:  pesetasAsset,
		Destinations: []txnbuild.Claimant{
			txnbuild.NewClaimant(accountKeyPair.Address(), nil),
		},
	}
	allOps = append(allOps, &pesetasCreateCB)
	itest.MustSubmitOperations(itest.MasterAccount(), itest.Master(), &pesetasCreateCB)

	listCBResp, err := itest.Client().ClaimableBalances(auroraclient.ClaimableBalanceRequest{
		Claimant: accountKeyPair.Address(),
	})
	tt.NoError(err)
	cbID := listCBResp.Embedded.Records[0].BalanceID

	// Clawback the claimable balance
	pesetasClawbackCB := txnbuild.ClawbackClaimableBalance{
		BalanceID: cbID,
	}
	allOps = append(allOps, &pesetasClawbackCB)
	txResp := itest.MustSubmitOperations(itest.MasterAccount(), itest.Master(), &pesetasClawbackCB)

	return allOps, txResp.Ledger
}

func submitClaimableBalanceOps(itest *integration.Test, tt *assert.Assertions) (submittedOperations []txnbuild.Operation, lastLedger int32) {

	// Create another account from which to claim an asset back
	keyPairs, accounts := itest.CreateAccounts(1, "100")
	accountKeyPair := keyPairs[0]
	account := accounts[0]

	// Make a claimable balance from the master account (and asset issuer) to the account with an asset which allows clawback
	createCB := txnbuild.CreateClaimableBalance{
		Amount: "10",
		Asset:  txnbuild.NativeAsset{},
		Destinations: []txnbuild.Claimant{
			txnbuild.NewClaimant(accountKeyPair.Address(), nil),
		},
	}
	allOps := []txnbuild.Operation{&createCB}
	itest.MustSubmitOperations(itest.MasterAccount(), itest.Master(), &createCB)

	listCBResp, err := itest.Client().ClaimableBalances(auroraclient.ClaimableBalanceRequest{
		Claimant: accountKeyPair.Address(),
	})
	tt.NoError(err)
	cbID := listCBResp.Embedded.Records[0].BalanceID

	// Claim the claimable balance
	claimCB := txnbuild.ClaimClaimableBalance{
		BalanceID: cbID,
	}
	allOps = append(allOps, &claimCB)
	txResp := itest.MustSubmitOperations(account, accountKeyPair, &claimCB)

	return allOps, txResp.Ledger
}

func submitOfferAndTrustlineOps(itest *integration.Test, tt *assert.Assertions) (submittedOperations []txnbuild.Operation, lastLedger int32) {
	// Create another account from which to claw an asset back
	keyPairs, accounts := itest.CreateAccounts(1, "100")
	accountKeyPair := keyPairs[0]
	account := accounts[0]

	// Add some assets to the account with asset which allows clawback

	// Time machine to Spain before Euros were a thing
	pesetasAsset := txnbuild.CreditAsset{Code: "PTS", Issuer: itest.Master().Address()}
	itest.MustEstablishTrustline(accountKeyPair, account, pesetasAsset)

	ops := []txnbuild.Operation{
		&txnbuild.ManageSellOffer{
			Selling: txnbuild.NativeAsset{},
			Buying:  pesetasAsset,
			Amount:  "10",
			Price:   xdr.Price{1, 1},
			OfferID: 0,
		},
		&txnbuild.ManageBuyOffer{
			Selling: txnbuild.NativeAsset{},
			Buying:  pesetasAsset,
			Amount:  "10",
			Price:   xdr.Price{1, 1},
			OfferID: 0,
		},
		&txnbuild.CreatePassiveSellOffer{
			Selling: txnbuild.NativeAsset{},
			Buying:  pesetasAsset,
			Amount:  "10",
			Price:   xdr.Price{1, 1},
		},
	}
	allOps := ops
	itest.MustSubmitOperations(itest.MasterAccount(), itest.Master(), ops...)

	ops = []txnbuild.Operation{
		&txnbuild.AllowTrust{
			Trustor:   account.GetAccountID(),
			Type:      pesetasAsset,
			Authorize: true,
		},
		&txnbuild.SetTrustLineFlags{
			Trustor:  account.GetAccountID(),
			Asset:    pesetasAsset,
			SetFlags: []txnbuild.TrustLineFlag{txnbuild.TrustLineAuthorized},
		},
	}
	allOps = append(allOps, ops...)
	txResp := itest.MustSubmitOperations(itest.MasterAccount(), itest.Master(), ops...)

	return allOps, txResp.Ledger
}

func submitAccountOps(itest *integration.Test, tt *assert.Assertions) (submittedOperations []txnbuild.Operation, lastLedger int32) {
	accountPair, _ := keypair.Random()

	ops := []txnbuild.Operation{
		&txnbuild.CreateAccount{
			Destination: accountPair.Address(),
			Amount:      "100",
		},
	}
	allOps := ops
	itest.MustSubmitOperations(itest.MasterAccount(), itest.Master(), ops...)
	account := itest.MustGetAccount(accountPair)
	domain := "www.example.com"
	ops = []txnbuild.Operation{
		&txnbuild.BumpSequence{
			BumpTo: account.Sequence + 1000,
		},
		&txnbuild.SetOptions{
			HomeDomain: &domain,
		},
		&txnbuild.ManageData{
			Name:  "foo",
			Value: []byte("bar"),
		},
	}
	allOps = append(allOps, ops...)
	itest.MustSubmitOperations(&account, accountPair, ops...)
	// Resync bump sequence
	account = itest.MustGetAccount(accountPair)

	ops = []txnbuild.Operation{
		&txnbuild.ManageData{
			Name:  "foo",
			Value: nil,
		},
		&txnbuild.AccountMerge{
			Destination: itest.Master().Address(),
		},
	}
	allOps = append(allOps, ops...)
	txResp := itest.MustSubmitOperations(&account, accountPair, ops...)

	return allOps, txResp.Ledger
}

func initializeDBIntegrationTest(t *testing.T) (itest *integration.Test, reachedLedger int32) {
	itest = integration.NewTest(t, integration.Config{})
	tt := assert.New(t)

	// submit all possible operations
	ops, _ := submitAccountOps(itest, tt)
	submittedOps := ops
	ops, _ = submitPaymentOps(itest, tt)
	submittedOps = append(submittedOps, ops...)
	ops, _ = submitOfferAndTrustlineOps(itest, tt)
	submittedOps = append(submittedOps, ops...)
	ops, _ = submitSponsorshipOps(itest, tt)
	submittedOps = append(submittedOps, ops...)
	ops, _ = submitClaimableBalanceOps(itest, tt)
	submittedOps = append(submittedOps, ops...)
	ops, _ = submitClawbackOps(itest, tt)
	submittedOps = append(submittedOps, ops...)
	ops, reachedLedger = submitLiquidityPoolOps(itest, tt)
	submittedOps = append(submittedOps, ops...)

	// Make sure all possible operations are covered by reingestion
	allOpTypes := set.Set[xdr.OperationType]{}
	for typ := range xdr.OperationTypeToStringMap {
		allOpTypes.Add(xdr.OperationType(typ))
	}
	// Inflation is not supported
	delete(allOpTypes, xdr.OperationTypeInflation)

	for _, op := range submittedOps {
		opXDR, err := op.BuildXDR()
		tt.NoError(err)
		delete(allOpTypes, opXDR.Body.Type)
	}
	tt.Empty(allOpTypes)

	root, err := itest.Client().Root()
	tt.NoError(err)
	tt.LessOrEqual(reachedLedger, root.AuroraSequence)

	return
}

func TestReingestDB(t *testing.T) {
	itest, reachedLedger := initializeDBIntegrationTest(t)
	tt := assert.New(t)

	auroraConfig := itest.GetAuroraIngestConfig()
	t.Run("validate parallel range", func(t *testing.T) {
		auroracmd.RootCmd.SetArgs(command(auroraConfig,
			"db",
			"reingest",
			"range",
			"--parallel-workers=2",
			"10",
			"2",
		))

		assert.EqualError(t, auroracmd.RootCmd.Execute(), "Invalid range: {10 2} from > to")
	})

	// cap reachedLedger to the nearest checkpoint ledger because reingest range cannot ingest past the most
	// recent checkpoint ledger when using captive core
	toLedger := uint32(reachedLedger)
	archive, err := historyarchive.Connect(auroraConfig.HistoryArchiveURLs[0], historyarchive.ConnectOptions{
		NetworkPassphrase:   auroraConfig.NetworkPassphrase,
		CheckpointFrequency: auroraConfig.CheckpointFrequency,
	})
	tt.NoError(err)

	// make sure a full checkpoint has elapsed otherwise there will be nothing to reingest
	var latestCheckpoint uint32
	publishedFirstCheckpoint := func() bool {
		has, requestErr := archive.GetRootHAS()
		tt.NoError(requestErr)
		latestCheckpoint = has.CurrentLedger
		return latestCheckpoint > 1
	}
	tt.Eventually(publishedFirstCheckpoint, 10*time.Second, time.Second)

	if toLedger > latestCheckpoint {
		toLedger = latestCheckpoint
	}

	// We just want to test reingestion, so there's no reason for a background
	// Aurora to run. Keeping it running will actually cause the Captive Core
	// subprocesses to conflict.
	itest.StopAurora()

	auroraConfig.CaptiveCoreConfigPath = filepath.Join(
		filepath.Dir(auroraConfig.CaptiveCoreConfigPath),
		"captive-core-reingest-range-integration-tests.cfg",
	)

	auroracmd.RootCmd.SetArgs(command(auroraConfig, "db",
		"reingest",
		"range",
		"--parallel-workers=1",
		"1",
		fmt.Sprintf("%d", toLedger),
	))

	tt.NoError(auroracmd.RootCmd.Execute())
	tt.NoError(auroracmd.RootCmd.Execute(), "Repeat the same reingest range against db, should not have errors.")
}

func command(auroraConfig aurora.Config, args ...string) []string {
	return append([]string{
		"--hcnet-core-url",
		auroraConfig.HcnetCoreURL,
		"--history-archive-urls",
		auroraConfig.HistoryArchiveURLs[0],
		"--db-url",
		auroraConfig.DatabaseURL,
		"--hcnet-core-db-url",
		auroraConfig.HcnetCoreDatabaseURL,
		"--hcnet-core-binary-path",
		auroraConfig.CaptiveCoreBinaryPath,
		"--captive-core-config-path",
		auroraConfig.CaptiveCoreConfigPath,
		"--captive-core-use-db=" +
			strconv.FormatBool(auroraConfig.CaptiveCoreConfigUseDB),
		"--enable-captive-core-ingestion=" + strconv.FormatBool(auroraConfig.EnableCaptiveCoreIngestion),
		"--network-passphrase",
		auroraConfig.NetworkPassphrase,
		// due to ARTIFICIALLY_ACCELERATE_TIME_FOR_TESTING
		"--checkpoint-frequency",
		"8",
		// Create the storage directory outside of the source repo,
		// otherwise it will break Golang test caching.
		"--captive-core-storage-path=" + os.TempDir(),
	}, args...)
}

func TestMigrateIngestIsTrueByDefault(t *testing.T) {
	tt := assert.New(t)
	// Create a fresh Aurora database
	newDB := dbtest.Postgres(t)
	freshAuroraPostgresURL := newDB.DSN

	auroracmd.RootCmd.SetArgs([]string{
		// ingest is set to true by default
		"--db-url", freshAuroraPostgresURL,
		"db", "migrate", "up",
	})
	tt.NoError(auroracmd.RootCmd.Execute())

	dbConn, err := db.Open("postgres", freshAuroraPostgresURL)
	tt.NoError(err)

	status, err := schema.Status(dbConn.DB.DB)
	tt.NoError(err)
	tt.NotContains(status, "1_initial_schema.sql\t\t\t\t\t\tno")
}

func TestMigrateChecksIngestFlag(t *testing.T) {
	tt := assert.New(t)
	// Create a fresh Aurora database
	newDB := dbtest.Postgres(t)
	freshAuroraPostgresURL := newDB.DSN

	auroracmd.RootCmd.SetArgs([]string{
		"--ingest=false",
		"--db-url", freshAuroraPostgresURL,
		"db", "migrate", "up",
	})
	tt.NoError(auroracmd.RootCmd.Execute())

	dbConn, err := db.Open("postgres", freshAuroraPostgresURL)
	tt.NoError(err)

	status, err := schema.Status(dbConn.DB.DB)
	tt.NoError(err)
	tt.Contains(status, "1_initial_schema.sql\t\t\t\t\t\tno")
}

func TestFillGaps(t *testing.T) {
	itest, reachedLedger := initializeDBIntegrationTest(t)
	tt := assert.New(t)

	// Create a fresh Aurora database
	newDB := dbtest.Postgres(t)
	freshAuroraPostgresURL := newDB.DSN
	auroraConfig := itest.GetAuroraIngestConfig()
	auroraConfig.DatabaseURL = freshAuroraPostgresURL
	// Initialize the DB schema
	dbConn, err := db.Open("postgres", freshAuroraPostgresURL)
	tt.NoError(err)
	historyQ := history.Q{dbConn}
	defer func() {
		historyQ.Close()
		newDB.Close()
	}()

	_, err = schema.Migrate(dbConn.DB.DB, schema.MigrateUp, 0)
	tt.NoError(err)

	// cap reachedLedger to the nearest checkpoint ledger because reingest range cannot ingest past the most
	// recent checkpoint ledger when using captive core
	toLedger := uint32(reachedLedger)
	archive, err := historyarchive.Connect(auroraConfig.HistoryArchiveURLs[0], historyarchive.ConnectOptions{
		NetworkPassphrase:   auroraConfig.NetworkPassphrase,
		CheckpointFrequency: auroraConfig.CheckpointFrequency,
	})
	tt.NoError(err)

	t.Run("validate parallel range", func(t *testing.T) {
		auroracmd.RootCmd.SetArgs(command(auroraConfig,
			"db",
			"fill-gaps",
			"--parallel-workers=2",
			"10",
			"2",
		))

		assert.EqualError(t, auroracmd.RootCmd.Execute(), "Invalid range: {10 2} from > to")
	})

	// make sure a full checkpoint has elapsed otherwise there will be nothing to reingest
	var latestCheckpoint uint32
	publishedFirstCheckpoint := func() bool {
		has, requestErr := archive.GetRootHAS()
		tt.NoError(requestErr)
		latestCheckpoint = has.CurrentLedger
		return latestCheckpoint > 1
	}
	tt.Eventually(publishedFirstCheckpoint, 10*time.Second, time.Second)

	if toLedger > latestCheckpoint {
		toLedger = latestCheckpoint
	}

	// We just want to test reingestion, so there's no reason for a background
	// Aurora to run. Keeping it running will actually cause the Captive Core
	// subprocesses to conflict.
	itest.StopAurora()

	var oldestLedger, latestLedger int64
	tt.NoError(historyQ.ElderLedger(context.Background(), &oldestLedger))
	tt.NoError(historyQ.LatestLedger(context.Background(), &latestLedger))
	tt.NoError(historyQ.DeleteRangeAll(context.Background(), oldestLedger, latestLedger))

	auroraConfig.CaptiveCoreConfigPath = filepath.Join(
		filepath.Dir(auroraConfig.CaptiveCoreConfigPath),
		"captive-core-reingest-range-integration-tests.cfg",
	)
	auroracmd.RootCmd.SetArgs(command(auroraConfig, "db", "fill-gaps", "--parallel-workers=1"))
	tt.NoError(auroracmd.RootCmd.Execute())

	tt.NoError(historyQ.LatestLedger(context.Background(), &latestLedger))
	tt.Equal(int64(0), latestLedger)

	auroracmd.RootCmd.SetArgs(command(auroraConfig, "db", "fill-gaps", "3", "4"))
	tt.NoError(auroracmd.RootCmd.Execute())
	tt.NoError(historyQ.LatestLedger(context.Background(), &latestLedger))
	tt.NoError(historyQ.ElderLedger(context.Background(), &oldestLedger))
	tt.Equal(int64(3), oldestLedger)
	tt.Equal(int64(4), latestLedger)

	auroracmd.RootCmd.SetArgs(command(auroraConfig, "db", "fill-gaps", "6", "7"))
	tt.NoError(auroracmd.RootCmd.Execute())
	tt.NoError(historyQ.LatestLedger(context.Background(), &latestLedger))
	tt.NoError(historyQ.ElderLedger(context.Background(), &oldestLedger))
	tt.Equal(int64(3), oldestLedger)
	tt.Equal(int64(7), latestLedger)
	var gaps []history.LedgerRange
	gaps, err = historyQ.GetLedgerGaps(context.Background())
	tt.NoError(err)
	tt.Equal([]history.LedgerRange{{StartSequence: 5, EndSequence: 5}}, gaps)

	auroracmd.RootCmd.SetArgs(command(auroraConfig, "db", "fill-gaps"))
	tt.NoError(auroracmd.RootCmd.Execute())
	tt.NoError(historyQ.LatestLedger(context.Background(), &latestLedger))
	tt.NoError(historyQ.ElderLedger(context.Background(), &oldestLedger))
	tt.Equal(int64(3), oldestLedger)
	tt.Equal(int64(7), latestLedger)
	gaps, err = historyQ.GetLedgerGaps(context.Background())
	tt.NoError(err)
	tt.Empty(gaps)

	auroracmd.RootCmd.SetArgs(command(auroraConfig, "db", "fill-gaps", "2", "8"))
	tt.NoError(auroracmd.RootCmd.Execute())
	tt.NoError(historyQ.LatestLedger(context.Background(), &latestLedger))
	tt.NoError(historyQ.ElderLedger(context.Background(), &oldestLedger))
	tt.Equal(int64(2), oldestLedger)
	tt.Equal(int64(8), latestLedger)
	gaps, err = historyQ.GetLedgerGaps(context.Background())
	tt.NoError(err)
	tt.Empty(gaps)
}

func TestResumeFromInitializedDB(t *testing.T) {
	itest, reachedLedger := initializeDBIntegrationTest(t)
	tt := assert.New(t)

	// Stop the integration test, and restart it with the same database
	err := itest.RestartAurora()
	tt.NoError(err)

	successfullyResumed := func() bool {
		root, err := itest.Client().Root()
		tt.NoError(err)
		// It must be able to reach the ledger and surpass it
		const ledgersPastStopPoint = 4
		return root.AuroraSequence > (reachedLedger + ledgersPastStopPoint)
	}

	tt.Eventually(successfullyResumed, 1*time.Minute, 1*time.Second)
}
