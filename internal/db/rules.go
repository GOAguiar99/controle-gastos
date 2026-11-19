package db

import "time"

// CategoryRule is a remembered merchant → category choice.
type CategoryRule struct {
	MerchantKey string
	Example     string
	CategoryID  int64
	Category    string
	UpdatedAt   time.Time
}

// CategoryRules lists the remembered choices, newest first.
func (s *Store) CategoryRules() ([]CategoryRule, error) {
	rows, err := s.query(`SELECT r.merchant_key, r.example, r.category_id, c.name, r.updated_at
		FROM category_rules r JOIN categories c ON c.id = r.category_id ORDER BY r.updated_at DESC, r.merchant_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CategoryRule
	for rows.Next() {
		var r CategoryRule
		if err := rows.Scan(&r.MerchantKey, &r.Example, &r.CategoryID, &r.Category, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveCategoryRule remembers (or replaces) the category of a merchant.
func (s *Store) SaveCategoryRule(key, example string, categoryID int64) error {
	return s.exec(`INSERT INTO category_rules (merchant_key, example, category_id) VALUES ($1, $2, $3)
		ON CONFLICT (ledger_id, merchant_key) DO UPDATE SET example = EXCLUDED.example, category_id = EXCLUDED.category_id, updated_at = now()`,
		key, example, categoryID)
}

// DeleteCategoryRule forgets a merchant's category (reports whether it existed).
func (s *Store) DeleteCategoryRule(key string) (bool, error) {
	var n int
	err := s.queryRow(`WITH d AS (DELETE FROM category_rules WHERE merchant_key = $1 RETURNING 1) SELECT count(*) FROM d`, key).Scan(&n)
	return n > 0, err
}

// SetTxCategory moves one transaction to another category.
func (s *Store) SetTxCategory(id, categoryID int64) error {
	return s.exec(`UPDATE transactions SET category_id = $1 WHERE id = $2`, categoryID, id)
}
