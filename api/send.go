package api

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"

	"github.com/cosmos/cosmos-sdk/client/flags"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/auth"
	"github.com/cosmos/cosmos-sdk/x/bank"

	core "github.com/terra-project/core/types"
)

// BankSendBody contains the necessary data to make a send transaction
type BankSendBody struct {
	Sender        sdk.AccAddress `json:"sender"`
	Reciever      sdk.AccAddress `json:"reciever"`
	Amount        string         `json:"amount"`
	ChainID       string         `json:"chain_id"`
	Memo          string         `json:"memo,omitempty"`
	Fees          string         `json:"fees,omitempty"`
	Gas           string         `json:"gas,omitempty"`
	GasPrices     string         `json:"gas_prices,omitempty"`
	GasAdjustment string         `json:"gas_adjustment,omitempty"`
}

// Marshal - nolint
func (sb BankSendBody) Marshal() []byte {
	out, err := json.Marshal(sb)
	if err != nil {
		panic(err)
	}
	return out
}

// BankSend handles the /tx/bank/send route
func (s *Server) BankSend(w http.ResponseWriter, r *http.Request) {
	var sb BankSendBody

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write(newError(err).marshal())
		return
	}

	err = cdc.UnmarshalJSON(body, &sb)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write(newError(err).marshal())
		return
	}

	coins, err := sdk.ParseCoins(sb.Amount)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write(newError(fmt.Errorf("failed to parse amount %s into sdk.Coins", sb.Amount)).marshal())
		return
	}

	var fees sdk.Coins
	if sb.Fees != "" {
		if sb.GasPrices != "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write(newError(fmt.Errorf("GasPrices and Fees cannot be used at the same time")).marshal())
			return
		}

		fees, err = sdk.ParseCoins(sb.Fees)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write(newError(fmt.Errorf("failed to parse fees %s into sdk.Coins", sb.Fees)).marshal())
			return
		}
	}

	// dummy fee & dummy gas limit
	var feesForSim sdk.Coins
	if fees.Empty() {
		feesForSim = sdk.NewCoins(sdk.NewCoin(coins[0].Denom, sdk.NewInt(1)))
	} else {
		feesForSim = sdk.NewCoins(fees...)
	}

	stdTx := auth.NewStdTx(
		[]sdk.Msg{bank.MsgSend{FromAddress: sb.Sender, ToAddress: sb.Reciever, Amount: coins}},
		auth.NewStdFee(flags.DefaultGasLimit, feesForSim),
		[]auth.StdSignature{{}},
		sb.Memo,
	)

	var gas uint64
	if sb.Gas != "" {
		gas, err = strconv.ParseUint(sb.Gas, 10, 64)
	} else {
		gas, err = s.SimulateGas(cdc.MustMarshalBinaryLengthPrefixed(stdTx))
	}

	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write(newError(fmt.Errorf("failed to parse gas %s into uint64; %s", sb.Gas, err.Error())).marshal())
		return
	}

	if gas != 0 && sb.GasAdjustment != "" {
		adj, err := strconv.ParseFloat(sb.GasAdjustment, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write(newError(fmt.Errorf("failed to parse gasAdjustment %s into float64", sb.GasAdjustment)).marshal())
			return
		}
		gas = uint64(adj * float64(gas))
	}

	if sb.GasPrices != "" {
		gasPrices, err := sdk.ParseDecCoins(sb.GasPrices)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write(newError(fmt.Errorf("failed to parse gasPrices %s into sdk.DecCoins", sb.GasPrices)).marshal())
			return
		}

		for _, gasPrice := range gasPrices {
			fee := sdk.NewCoin(gasPrice.Denom, gasPrice.Amount.MulInt64(int64(gas)).Ceil().TruncateInt())
			fees = append(fees, fee)
		}

		fees = fees.Sort()
	}

	if sb.Fees == "" {
		// Compute Tax
		taxRate, err := s.LoadTaxRate()
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)

			w.Write([]byte(sdkerrors.Wrap(err, "failed to load tax rate").Error()))
			return
		}

		var taxes sdk.Coins
		for _, coin := range coins {
			if coin.Denom == core.MicroLunaDenom {
				continue
			}

			taxCap, err := s.LoadTaxCap(coin.Denom)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)

				w.Write([]byte(sdkerrors.Wrap(err, "failed to load tax cap").Error()))
				return
			}

			taxDue := taxRate.MulInt(coin.Amount).TruncateInt()
			if taxDue.GT(taxCap) {
				taxDue = taxCap
			}

			taxes = append(taxes, sdk.NewCoin(coin.Denom, taxDue))
		}

		taxes = taxes.Sort()
		fees = taxes.Add(fees...)
	}

	stdTx = auth.NewStdTx(
		stdTx.Msgs,
		auth.NewStdFee(gas, fees),
		[]auth.StdSignature{},
		stdTx.Memo,
	)

	w.WriteHeader(http.StatusOK)
	w.Write(cdc.MustMarshalJSON(stdTx))
	return
}
