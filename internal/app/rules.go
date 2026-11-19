package app

import (
	"strings"

	"controle-gastos/internal/db"
	"controle-gastos/internal/finance"
)

// MerchantKey is how purchases from the same merchant are recognized:
// the description normalized (case, accents, digits, "Vmt*"/"Ifd*" prefixes).
func MerchantKey(desc string) string { return descKey(desc) }

// RecategorizeResult says what a category correction changed.
type RecategorizeResult struct {
	Tx         finance.Entry
	Category   string
	Others     int    // other purchases from the same merchant moved along
	Remembered bool   // future purchases from the merchant get this category
	Merchant   string // the description the memory is keyed on
}

// Recategorize files an expense under another category and, with remember,
// teaches the app: the merchant's other purchases still in the old category
// move along (ones the user filed elsewhere stay), and later purchases from
// the merchant are suggested this category.
func (s *Service) Recategorize(id int64, ref CategoryRef, remember bool) (RecategorizeResult, error) {
	var res RecategorizeResult
	e, err := s.st.Tx(id)
	if err != nil {
		return res, notFound(err, "transação")
	}
	if e.Kind != finance.Expense {
		return res, invalid("só gastos têm categoria de compra")
	}
	c, err := s.resolveCategory(finance.Expense, ref)
	if err != nil {
		return res, err
	}
	res.Category = c.Name
	key := MerchantKey(e.Description)
	if key == "" {
		remember = false // nothing to recognize the merchant by
	}
	err = s.st.InTx(func(st *db.Store) error {
		if err := st.SetTxCategory(id, c.ID); err != nil {
			return err
		}
		if !remember {
			return nil
		}
		all, err := st.AllTransactions()
		if err != nil {
			return err
		}
		for _, o := range all {
			if o.ID == id || o.Kind != finance.Expense || o.CategoryID != e.CategoryID || o.CategoryID == c.ID || MerchantKey(o.Description) != key {
				continue
			}
			if err := st.SetTxCategory(o.ID, c.ID); err != nil {
				return err
			}
			res.Others++
		}
		return st.SaveCategoryRule(key, strings.TrimSpace(e.Description), c.ID)
	})
	if err != nil {
		return res, err
	}
	res.Remembered, res.Merchant = remember, strings.TrimSpace(e.Description)
	res.Tx, err = s.st.Tx(id)
	return res, err
}

// CategoryRules lists what the app remembers.
func (s *Service) CategoryRules() ([]db.CategoryRule, error) { return s.st.CategoryRules() }

// RememberCategory teaches the app the category of a merchant directly
// (without changing past purchases).
func (s *Service) RememberCategory(merchant string, ref CategoryRef) (db.CategoryRule, error) {
	key := MerchantKey(merchant)
	if key == "" {
		return db.CategoryRule{}, invalid("informe o nome do estabelecimento como aparece na fatura")
	}
	c, err := s.resolveCategory(finance.Expense, ref)
	if err != nil {
		return db.CategoryRule{}, err
	}
	if err := s.st.SaveCategoryRule(key, strings.TrimSpace(merchant), c.ID); err != nil {
		return db.CategoryRule{}, err
	}
	return db.CategoryRule{MerchantKey: key, Example: strings.TrimSpace(merchant), CategoryID: c.ID, Category: c.Name}, nil
}

// ForgetCategory deletes the memory of a merchant (by its key or by a
// description that normalizes to it).
func (s *Service) ForgetCategory(merchant string) error {
	key := strings.TrimSpace(merchant)
	ok, err := s.st.DeleteCategoryRule(key)
	if err == nil && !ok {
		ok, err = s.st.DeleteCategoryRule(MerchantKey(merchant))
	}
	if err != nil {
		return err
	}
	if !ok {
		return invalid("nenhuma memória para %q", merchant)
	}
	return nil
}
