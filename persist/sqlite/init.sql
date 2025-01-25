CREATE TABLE address_balances (
    id INTEGER PRIMARY KEY,
    address BLOB UNIQUE NOT NULL,
    siacoin_balance BLOB NOT NULL,
    is_foundation BOOL NOT NULL DEFAULT false
);

CREATE INDEX address_balances_is_foundation ON address_balances (is_foundation);

CREATE TABLE global_settings (
    id INTEGER PRIMARY KEY NOT NULL DEFAULT 0 CHECK (id = 0), -- enforce a single row
    db_version INTEGER NOT NULL, -- used for migrations
    total_supply BLOB NOT NULL, -- the total supply of Siacoin
    circulating_supply BLOB NOT NULL, -- the circulating supply of Siacoin
    burned_supply BLOB NOT NULL, -- the supply that has been verifiably burned
    last_indexed_height INTEGER NOT NULL, -- the height of the last chain index that was processed
    last_indexed_id BLOB NOT NULL -- the block ID of the last chain index that was processed
);
