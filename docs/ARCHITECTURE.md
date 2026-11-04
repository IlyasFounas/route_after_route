# Architecture

## Stack

**Go**, stdlib uniquement. Raisons :

- Le binaire est autonome et multiplateforme : `go build` suffit après le clone.
- La contrainte « interface commune obligatoire » devient un `interface` Go vérifié à la compilation.
- Les frontières entre couches sont des packages `internal/`. Un test (`internal/archtest`) fait échouer la CI dès qu'une couche importe ce qu'elle ne doit pas.
- Les couples (marché, stratégie) sont évalués en parallèle avec des goroutines.
- L'argent est en `int64`, en unités mineures. Les flottants ne servent qu'aux prix, signaux et scores.

## Les 7 couches

```
cmd/rar ─► internal/app (racine de composition : seul endroit qui connaît les adaptateurs)
              │
   1 profile ─► 2 market ─► 3 strategy ─► 4 eval ─► 5 arbiter ─► 6 output ─► 7 loop
                  ▲  (interface)              ▲                                  │
     market/csvfile, market/binance      backtest (banc d'essai)                 │
                                                                                 │
              capital du cycle n+1 = valeur du portefeuille ◄────────────────────┘
```

| # | Package | Contrat |
|---|---|---|
| 1 | `profile` | `Profile{Capital, HoursPerWeek, Skills, RiskTolerance}`. Décodage strict : champs inconnus, champs manquants, données en trop et valeurs hors bornes sont rejetés. Fuzzé. |
| 2 | `market` | `Market{Descriptor(); Bars(ctx, symbol, from, to)}`. `Descriptor` déclare l'id, le type d'actif, la granularité, les frais, le capital minimum, les contraintes d'accès et les symboles, délistés compris. `Select` filtre par compatibilité avec le profil. **Contrat figé.** |
| 3 | `strategy` | `Strategy{Spec(); Signal(history) Opinion}`. `Spec` est déclaratif : données consommées, prérequis, paramètres. `Opinion{Signal ∈ [-1,1], Confidence, Trace}`. Une stratégie émet un avis, elle ne décide pas. |
| 4 | `eval` | Un score par couple valide, avec un **critère unique** (voir plus bas). Le score est accompagné de toutes les valeurs intermédiaires (`Trace`). |
| 5 | `arbiter` | Filtre, tri déterministe, puis répartition du capital (`Policy`). |
| 6 | `output` | `Report` : actions classées (marché, stratégie, allocation, score, trace, signal par symbole), ordres, et candidats et marchés rejetés avec leurs motifs. Sortie texte ou JSON. |
| 7 | `loop` | `State` persisté dans `state.json`, conditions de sortie, réinjection de l'équité comme capital au cycle suivant, fréquence via `reevaluate_every`. |

### Règle de dépendances (vérifiée par `internal/archtest`)

| Couche | Peut importer |
|---|---|
| profile | — |
| market | profile |
| strategy | market, profile |
| backtest | market, strategy |
| eval | market, strategy, backtest |
| arbiter | — |
| output | profile, market, arbiter |
| loop | profile, output |

Aucune couche n'importe `market/<adaptateur>` ni `app`. Les adaptateurs s'enregistrent via `market.Register` dans leur `init()`, et seul `cmd/rar` les importe.

## Couche 4 : critère de score

**`oos_sharpe`** : ratio de Sharpe annualisé (taux sans risque = 0) des rendements **hors échantillon** du walk-forward. Les rendements sont nets des frais taker du marché et du slippage configuré. Ils sont regroupés sur **tous les symboles négociables pendant la période, délistés compris**.

L'annualisation dépend du type d'actif : crypto 365 j × 24 h, actions 252 séances × 6,5 h, FX 260 j × 24 h.

## Couche 5 : arbitrage

1. **Filtrage**. Un candidat est rejeté si :
   - un prérequis n'est pas rempli, les données manquent ou l'évaluation a échoué ;
   - son score n'est pas fini ;
   - son score est inférieur à `min_score` ;
   - son score est ≤ 0 alors que la règle pondère par le score ;
   - son drawdown backtesté dépasse la limite du profil.
2. **Tri** par score décroissant. Les égalités sont départagées par l'id du marché, puis celui de la stratégie.
3. **Allocation** sur les `max_active` premiers :
   - `equal` : parts égales ;
   - `proportional` : poids proportionnel au score ;
   - `capped` : proportionnel, plafonné à `cap`. L'excédent est redistribué aux candidats non plafonnés, puis laissé en cash s'il n'en reste plus.
4. Si une part tombe sous le capital minimum de son marché, le candidat le moins bien classé concerné est retiré et le suivant prend sa place. La répartition est alors recalculée.

Dans une action, l'allocation se répartit à parts égales entre les symboles dont le signal n'est pas plat. Chaque part est ensuite pondérée par `|exposition|`.

**Valeurs par défaut** : `max_active = 3`, `capped` avec `cap = 0.5`, `min_score = 0`.

## Couche 7 : boucle

Les conditions de sortie sont testées dans cet ordre :

1. le stop-loss (la moitié du drawdown maximal du profil) ;
2. le couple n'est plus sélectionné ;
3. le signal a changé de direction.

Une position fermée par stop-loss n'est pas rouverte dans le même cycle. Les positions conservées ne sont pas redimensionnées, et les nouvelles sont financées sur le cash disponible.

## Banc d'essai (`backtest`)

- **Pas de lookahead.** La décision prise sur `bars[0..t]` est exécutée à l'**ouverture** de `t+1`. La stratégie reçoit `bars[:t+1:t+1]` : la capacité limitée l'empêche d'atteindre les barres futures, et un test le vérifie.
- **Frais et slippage.** Coût = `|Δexposition| × (frais + slippage)`. Les frais viennent du `Descriptor`, jamais de l'utilisateur. La vente à découvert n'est possible que si le marché l'autorise.
- **Walk-forward.** Fenêtres glissantes `train_bars` + `test_bars`, qui avancent de `test_bars` : les segments de test ne se chevauchent pas. `train_bars` doit être ≥ au lookback de la stratégie. Pour l'instant, le segment train sert au chauffage des indicateurs. Le type `Builder` est prévu pour ajuster les paramètres dessus.
- **Fin de période.** Toute position est fermée à la fin de chaque fold.
- **Biais de survivance.** Les symboles délistés restent déclarés, avec la date de délisting, et sont inclus dans l'évaluation. Le jeu de démo contient `DEAD`, délisté après 600 jours.

## Points ouverts

- **Granularité du score.** Le score est calculé par (marché, stratégie) et regroupe tous les symboles. Une stratégie excellente sur un symbole et perdante sur un autre obtient un score moyen. Il faut choisir entre ce modèle et un score par (marché, symbole, stratégie).
- **Combinaisons de critères.** Sortino, Calmar, pénalité de turnover…
- **Ajustement des paramètres** sur le segment train (`backtest.Builder`).
- **Redimensionnement** des positions conservées entre deux cycles.
