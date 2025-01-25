package sqlite

import (
	"database/sql"
	"errors"
	"fmt"

	"go.sia.tech/cmc-supply-api/index"
	"go.sia.tech/core/types"
)

const (
	pruneThreshold = 1000
)

type updateTxn struct {
	tx *txn
}

func (s *Store) UpdateState(state index.State, addressDeltas []index.AddressDelta, foundationAddresses []types.Address) error {
	return s.transaction(func(tx *txn) error {
		if len(foundationAddresses) > 0 {
			insertAddressStmt, err := tx.Prepare(`INSERT INTO address_balances (address, siacoin_balance, is_foundation) VALUES ($1, $2, true) ON CONFLICT (address) DO UPDATE SET is_foundation=true`)
			if err != nil {
				return fmt.Errorf("failed to prepare statement: %w", err)
			}
			defer insertAddressStmt.Close()

			for _, addr := range foundationAddresses {
				_, err = insertAddressStmt.Exec(encode(addr), encode(types.ZeroCurrency))
				if err != nil {
					return fmt.Errorf("failed to insert foundation address: %w", err)
				}
			}
		}

		if len(addressDeltas) != 0 {
			selectStmt, err := tx.Prepare(`SELECT siacoin_balance FROM address_balances WHERE address=$1`)
			if err != nil {
				return fmt.Errorf("failed to prepare select statement: %w", err)
			}
			defer selectStmt.Close()

			updateStmt, err := tx.Prepare(`INSERT INTO address_balances (address, siacoin_balance) VALUES ($1, $2) ON CONFLICT (address) DO UPDATE SET siacoin_balance=EXCLUDED.siacoin_balance`)
			if err != nil {
				return fmt.Errorf("failed to prepare update statement: %w", err)
			}
			defer updateStmt.Close()

			for _, delta := range addressDeltas {
				var balance types.Currency
				err = selectStmt.QueryRow(encode(delta.Address)).Scan(decode(&balance))
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("failed to get current balance: %w", err)
				}
				balance = balance.Add(delta.Incoming).Sub(delta.Outgoing)

				if res, err := updateStmt.Exec(encode(delta.Address), encode(balance)); err != nil {
					return fmt.Errorf("failed to update balance: %w", err)
				} else if n, _ := res.RowsAffected(); n != 1 {
					return errors.New("balance not updated")
				}
			}
		}

		_, err := tx.Exec(`UPDATE global_settings SET (total_supply, circulating_supply, burned_supply, last_indexed_height, last_indexed_id) = ($1, $2, $3, $4, $5)`, encode(state.TotalSupply), encode(state.CirculatingSupply), encode(state.BurnedSupply), state.Index.Height, encode(state.Index.ID))
		return err
	})
}

// State returns the current state
func (s *Store) State() (state index.State, err error) {
	err = s.transaction(func(tx *txn) error {
		return tx.QueryRow(`SELECT last_indexed_id, last_indexed_height, total_supply, circulating_supply, burned_supply FROM global_settings`).Scan(decode(&state.Index.ID), &state.Index.Height, decode(&state.TotalSupply), decode(&state.CirculatingSupply), decode(&state.BurnedSupply))
	})
	return
}

// FoundationTreasury returns the current value of the foundation treasury
func (s *Store) FoundationTreasury() (value types.Currency, err error) {
	err = s.transaction(func(tx *txn) error {
		const query = `SELECT siacoin_balance FROM address_balances WHERE is_foundation=true`

		rows, err := tx.Query(query)
		if err != nil {
			return fmt.Errorf("failed to query foundation balance: %w", err)
		}
		defer rows.Close()

		var balance types.Currency
		for rows.Next() {
			if err := rows.Scan(decode(&balance)); err != nil {
				return fmt.Errorf("failed to scan balance: %w", err)
			}
			value = value.Add(balance)
		}
		return rows.Err()
	})
	return
}
