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
| live | app, market, strategy, eval, arbiter, output, loop, profile |
| gui | live, output, loop, market, profile, arbiter, wallet |
| wallet | — |

Aucune couche n'importe `market/<adaptateur>`, et seul `live` importe `app`. Les adaptateurs s'enregistrent via `market.Register` dans leur `init()`, et seuls les binaires (`cmd/rar`, `cmd/rar-desktop`) les importent.

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

1. le stop-loss (la moitié du drawdown maximal du profil), ou la durée maximale de détention `Rules.MaxHold` ;
2. le couple n'est plus sélectionné ;
3. le signal a changé de direction.

Une position fermée par stop-loss ou par durée maximale n'est pas rouverte dans le même sens tant que son signal n'est pas redevenu plat ou ne s'est pas inversé.

**Historique.** Chaque position fermée, quelle qu'en soit la raison (`Advance`, `CheckStops`, `CloseAll`), est ajoutée à `State.Trades` : prix et heures d'entrée et de sortie, montant, frais, gain net, motif. Elle est sauvegardée avec l'état. L'historique n'est jamais modifié en place : chaque nouvel état l'étend avec `slices.Clip` puis `append`, donc les snapshots peuvent le partager sans copie. Un reset du registre ou un *Sync ledger* le conserve.

**Coûts.** `Rules.CostBps` donne le coût par côté de chaque marché, en points de base. Le moteur temps réel le calcule comme le backtest : frais taker plus `slippage_bps` (`live.CostBps`). Il est prélevé à la fermeture sur les deux côtés, `(montant + valeur de sortie) × coût`, et le gain net en tient compte. Le cycle du CLI ne le fixe pas : ses coûts restent dans le backtest.

**Porte d'entrée.** `Rules.AllowOpen`, quand elle est définie, est consultée avant chaque ouverture. Une entrée refusée n'est pas ouverte : elle sort en ordre `proposal`, sans toucher au cash. Les sorties ne passent jamais par cette porte. Le CLI et le backtest la laissent à `nil` (tout est autorisé) ; le moteur temps réel s'en sert pour l'approbation de l'utilisateur. Les positions conservées ne sont pas redimensionnées, et les nouvelles sont financées sur le cash disponible.

## Banc d'essai (`backtest`)

- **Pas de lookahead.** La décision prise sur `bars[0..t]` est exécutée à l'**ouverture** de `t+1`. La stratégie reçoit `bars[:t+1:t+1]` : la capacité limitée l'empêche d'atteindre les barres futures, et un test le vérifie.
- **Frais et slippage.** Coût = `|Δexposition| × (frais + slippage)`. Les frais viennent du `Descriptor`, jamais de l'utilisateur. La vente à découvert n'est possible que si le marché l'autorise.
- **Walk-forward.** Fenêtres glissantes `train_bars` + `test_bars`, qui avancent de `test_bars` : les segments de test ne se chevauchent pas. `train_bars` doit être ≥ au lookback de la stratégie. Pour l'instant, le segment train sert au chauffage des indicateurs. Le type `Builder` est prévu pour ajuster les paramètres dessus.
- **Fin de période.** Toute position est fermée à la fin de chaque fold.
- **Biais de survivance.** Les symboles délistés restent déclarés, avec la date de délisting, et sont inclus dans l'évaluation. Le jeu de démo contient `DEAD`, délisté après 600 jours.

## Moteur temps réel (`live`)

Le moteur fait tourner le pipeline en continu. Il réutilise `app.Fetch`, `app.Evaluate` (couches 1 à 5, coûteuses) et `Evaluation.Actions` (couche 6, peu coûteuse), sans dupliquer de logique de décision.

- **Flux.** `market.Streamer` est une interface **optionnelle**, détectée par assertion de type : le contrat `Market` ne change pas. Un marché qui ne l'implémente pas est interrogé périodiquement via `Bars`. Il y a deux implémentations :
  - `binance` : WebSocket des klines, reconnexion avec backoff exponentiel ;
  - `csvfile` : mode replay, qui rejoue les bougies postérieures à une date avec un intervalle donné.
- **Mise à jour de prix** (bougie en formation) : rafraîchissement du prix, valorisation du portefeuille, `loop.CheckStops`.
- **Bougie clôturée** : ajout à l'historique en mémoire, puis `Evaluation.Actions` et `loop.Advance`.
- **Réévaluation complète** tous les `reevaluate_every`, en temps moteur : `app.Decide`, après élagage de l'historique hors fenêtre. Seule l'évaluation est gardée : le portefeuille avance ensuite par `advance()`, donc une réévaluation ne peut pas ouvrir de position sans approbation.
- **Reconnexion** : les bougies manquées sont récupérées via `Bars`. Les doublons et les bougies hors ordre sont ignorés.
- **Horloge.** Le temps du moteur est l'heure de clôture de la dernière bougie reçue, pas l'heure murale. Un replay donne donc toujours le même résultat, ce que vérifie un test.
- **Concurrence.** Tout l'état appartient à une seule goroutine, la boucle d'événements. Chaque marché a sa propre goroutine de flux (`follow`). L'extérieur envoie des commandes (réévaluer, exécution, profil, reset, approuver, refuser, focus) et reçoit des `Snapshot` immuables, limités à 4 par seconde. Seule la série de la crypto affichée (`Options.Focus`) est publiée en entier ; les autres n'ont que leurs `TailBars` dernières bougies.
- **Cooldown.** Après un stop-loss, `loop.State.Cooldown` empêche de rouvrir la position dans le même sens tant que le signal n'est pas redevenu plat ou ne s'est pas inversé.
- **Persistance.** L'état est sauvegardé après chaque ordre et à l'arrêt.

### Approbation des entrées

Le moteur **n'ouvre jamais de position sans l'accord de l'utilisateur** :

1. Une entrée voulue par l'algorithme sort de `loop.Advance` en `proposal` et devient une `Opportunity` : identifiant, crypto, sens, signal, prix, montant, échéance. Elle est publiée dans `Snapshot.Opportunities` et journalisée (`EventOpportunity`).
2. L'échéance est en **heure murale** (`ApprovalTTL`, 10 s par défaut), car c'est le temps de réaction de l'utilisateur.
3. `Engine.Approve(id)` autorise cette entrée, puis relance aussitôt `advance()`. La position s'ouvre au dernier prix, seulement si l'exécution est autorisée, que l'opportunité n'a pas expiré et que le signal va toujours dans le même sens.
4. Une opportunité refusée (`Reject`) ou expirée n'est plus proposée tant que son signal n'est pas redevenu plat ou ne s'est pas inversé. Sinon elle reviendrait à chaque bougie de 1 s.
5. Les sorties (stop-loss, `MaxHold`, changement de signal, désélection) sont automatiques.

`Options.SkipApproval` n'existe que pour les tests : aucune application ne l'active.

## Interface de bureau (`gui`, `cmd/rar-desktop`)

L'interface est minimale : l'algorithme surveille, l'utilisateur décide de chaque entrée.

**Écrans** (`gui/screens.go`). La fenêtre empile trois écrans et n'en montre qu'un :

- **Accueil** : une seule invite (`>`), où l'on tape une commande. `trading` ouvre l'algorithme, `historic` l'historique. Un préfixe sans ambiguïté suffit.
- **Retour** : Échap ou l'icône maison ramènent à l'invite.
- **Ajouter un écran** : une entrée de plus dans `Commands()`.
- Le moteur tourne quel que soit l'écran affiché, et les notifications continuent.

L'écran **trading** tient en deux panneaux.

- **À gauche**, le cours de la crypto affichée : sélecteur (toutes les paires XXX/USDT de Binance, avec recherche), prix, variation sur la période et courbe.
  - Périodes courtes (1 min, 5 min, 1 h) : tirées des bougies de 1 s du moteur.
  - Périodes longues (1 J, 1 S, 1 M) : bougies de 5 min, 1 h et 4 h demandées à Binance pour l'affichage seulement (`Backend.History`, cache d'une minute).
  - La courbe est un raster dessiné pixel par pixel (`gui/chart.go`) : ligne anticrénelée, dégradé dessous, verte si la période monte et corail si elle baisse, avec survol, ordres du bot et prix d'entrée.
- **À droite**, quatre cartes (`gui/opportunities.go` pour les deux premières) :
  - **Opportunités** : les entrées en attente, avec compte à rebours et boutons *Accept* / *Ignore*. Les lignes ne sont reconstruites que si l'ensemble change, pour ne pas perdre un clic ;
  - **Algorithme** : le conseil en un mot (*Buy*, *Hold*, *Sell*, *Wait*, déduit de la décision par `gui/advice.go`), l'interrupteur d'autorisation et le registre (devnet ou démo) ;
  - **Watchlist** : les favoris, avec prix, variation sur 5 min et état (surveillé, opportunité, en position) ;
  - **Portefeuille** : choix *Public* / *Devnet*, puis le portefeuille correspondant.
- **Écran historic** : résumé (nombre de trades, gagnés et perdus, résultat net, frais, meilleur et pire trade), puis toutes les transactions fermées, la plus récente en premier.
- **Logos** (`gui/logos.go`) : chaque badge affiche d'abord les lettres du ticker. Le logo est demandé en arrière-plan (`Backend.Logo`, six téléchargements au plus en même temps), découpé en disque, puis remplace le badge à l'écran. Côté session, il vient du CDN de logos de Binance et est mis en cache dans `<config>/logos/`. Une erreur ne change rien : les lettres restent. Les tickers s'affichent `$SOL`, et la recherche accepte ce format.
- **Notifications** : chaque nouvelle opportunité déclenche une notification système, fenêtre visible ou non. Le menu de la barre système propose aussi une entrée *Accept* par opportunité. Les ordres exécutés ne sont notifiés que fenêtre masquée.
- **« Why? »** ouvre la trace complète en langage clair (`gui/why.go`) : stratégie en charge et sa description, score et drawdown hors échantillon, lecture actuelle des indicateurs, stratégies écartées avec leurs motifs, activité récente du bot. La traçabilité reste totale, mais elle n'encombre plus l'écran.
- **Thème** : `gui/theme.go`, chaud (crème, terracotta, ambre) et arrondi, avec une variante sombre (expresso, ambre) qui suit le système.

L'interface ne calcule aucune décision : elle affiche des snapshots et transmet les choix de l'utilisateur à une interface `gui.Backend`.

`cmd/rar-desktop/session.go` implémente ce `Backend`. Il possède le moteur, et c'est le seul endroit qui connaît les adaptateurs.

- **Favoris.** Chaque crypto favorite (10 au plus) devient **un marché Binance à elle** : `binance-spot:<PAIRE>`, en bougies de 1 s. Chacune a donc son WebSocket, sa goroutine de flux et sa propre sélection de stratégie, évaluée en parallèle par `app.Evaluate`. `policy.max_active` est relevé pour que chaque favori puisse émettre des signaux. La config est réduite à 2 h d'historique (`period.lookback`) et à une réévaluation toutes les 5 min.
- **Afficher un favori** ne fait que changer le focus du moteur. Ajouter ou retirer un favori redémarre le moteur ; les positions sur un favori retiré sont soldées à leur dernier prix (`settleForeign`).
- **Les réglages** (crypto affichée, favoris, période, autorisation, mode du portefeuille, adresse publique, capital du registre, délais) sont dans `settings.json`. La config (`config.json`) ne sert plus que de modèle pour les stratégies et le backtest.
- **Le portefeuille** est relu toutes les 2 minutes ou à la demande, et valorisé au prix Binance.

Fermer la fenêtre la masque seulement : le moteur continue derrière l'icône de la barre système.

### Niveaux d'effort

`live.Effort` (`internal/live/effort.go`) a cinq niveaux, comme `/effort` dans Claude Code : `low`, `medium`, `high` (par défaut), `xhigh`, `max`. Chaque niveau donne des `EffortParams` :

| Niveau | GOMAXPROCS | Snapshots | Réévaluation complète | Échantillonnage des brackets | nice |
|---|---|---|---|---|---|
| low | 1 | 1/s | 30 min | 1 fenêtre sur 8 | +10 |
| medium | 2 | 2/s | 15 min | 1 sur 4 | +5 |
| high | max(2, NumCPU/2) | 4/s | 5 min | 1 sur 2 | 0 |
| xhigh | NumCPU | 4/s | 2 min | toutes | 0 |
| max | NumCPU | 8/s | 1 min | toutes | 0 |

- **Ce qui ne change jamais** : chaque bougie de 1 s est lue, les stops et la durée maximale sont vérifiés à chaque mise à jour de prix. Le niveau ne change pas les décisions prises sur une bougie, seulement la fréquence du travail coûteux.
- **Moteur** : `Engine.SetPace(Pace{SnapshotEvery, ReevaluateEvery})`, une commande de plus. Le ticker des snapshots est réinitialisé. La prochaine réévaluation garde le temps déjà attendu. Un champ nul garde la valeur courante.
- **Session** : `SetEffort` enregistre le niveau dans `settings.json`, applique `runtime.GOMAXPROCS` (qui borne aussi le sémaphore de `app.Evaluate`), la priorité système, puis `SetPace`, sans redémarrer le moteur. La démo garde la période de réévaluation de sa config.
- **Priorité système** (`cmd/rar-desktop/priority_*.go`) : `setpriority` sur chaque thread (`/proc/self/task`) sous Linux, sur le processus sous macOS et BSD, `SetPriorityClass` (*below normal*, jamais *idle*) sous Windows. Revenir à une priorité normale demande des droits sous Unix : la session le signale, et cela s'applique au prochain lancement.
- **Interface** : la commande `effort` de l'invite (`effort` ouvre le sélecteur, `effort low` fixe le niveau), un sélecteur à cinq crans (←/→, Entrée, Échap) et un indicateur à cinq barres dans la barre du haut.

Au premier lancement, les fichiers sont créés à partir des exemples embarqués (`assets.go`). L'ancienne config replay est mise de côté (`config.replay-old.json`), avec son état. Le replay hors-ligne reste disponible avec `--demo`.

Le CLI `rar` reste sans CGO. Seul le binaire de bureau en dépend.

## Autorisation d'exécution et modes du portefeuille

`live.Options.Execute` et `Engine.SetExecution` contrôlent l'exécution. Elle est désactivée par défaut et le réglage est persisté.

- **Désactivée** : le moteur continue d'évaluer, de publier ses actions et de proposer des opportunités (à titre de conseil). Aucune position n'est ouverte, aucun stop-loss n'est exécuté et aucun ordre n'est journalisé.
- **Activée** : le bot peut prendre les entrées que l'utilisateur approuve, et gère seul les sorties.

L'application de bureau ne permet l'exécution que dans certains modes :

| Mode | Portefeuille affiché | Le bot peut trader ? | Registre |
|---|---|---|---|
| **Public** | adresse publique mainnet, en lecture seule | non : conseils et notifications seulement | aucun |
| **Devnet** | portefeuille devnet généré par l'app (`devnet-wallet.json`, format Solana CLI, `0600`) | oui, entrée par entrée, avec approbation | `devnet-ledger.json` |
| **Démo** (`--demo`, `--config`) | — | oui, avec approbation | `state.json` du dossier de la config |

**Registre devnet.** Aucun DEX du devnet n'a de liquidité réelle : les entrées approuvées sont donc exécutées au **prix Binance réel**, dans un registre local (`loop.State`).

- Son capital de départ est la valeur du portefeuille devnet (SOL de test × prix réel du SOL), figée par *Sync ledger*.
- Il faut au moins 10 $. En dessous, l'exécution est refusée.
- Quitter le devnet solde les positions ouvertes au dernier prix et coupe l'autorisation.

`live.Engine.recordOrders` reste le point unique où brancher plus tard un vrai exécuteur, par exemple un swap on-chain.

## Portefeuille (`wallet`)

Le package `wallet` lit un portefeuille en **lecture seule**, à partir d'une adresse publique uniquement.

La seule clé privée qu'il manipule est la clé de test **devnet** générée par l'app (`devnet.go`, `crypto/ed25519` de la bibliothèque standard), qui ne détient que du SOL de test sans valeur. `RequestAirdrop` demande 1 SOL de test au faucet du devnet. Le faucet public est très limité en débit : en cas de refus, l'app renvoie vers faucet.solana.com.

- **Validation** : l'adresse doit être en base58 et décoder en 32 octets.
- **Lecture** : JSON-RPC `getBalance` pour les SOL, puis `getTokenAccountsByOwner` sur les programmes Token et Token-2022 pour les jetons. Les soldes d'un même jeton répartis sur plusieurs comptes sont additionnés.
- **Jetons connus** : une table associe les mints courants (USDC, USDT, EURC, JUP, BONK, WIF, PYTH, JTO, RAY…) à une paire Binance.
- **Ce qui n'est pas valorisé** : les jetons inconnus et les jetons de staking liquide (mSOL, JitoSOL). Les valoriser au prix du SOL serait faux.

Le package ne dépend d'aucune autre couche.

## Distribution

`.github/workflows/release.yml` se déclenche sur un tag `v*` :

- **Application de bureau** : un runner natif par système (Fyne a besoin de CGO), puis `fyne package` pour produire une archive `.tar.xz` Linux, un `.app` macOS arm64 et amd64, et un `.exe` Windows.
- **CLI** : compilé en croisé depuis Linux.

Tout est attaché à la release GitHub. Les binaires ne sont pas signés.

## Points ouverts

- **Granularité du score.** Le score est calculé par (marché, stratégie) et regroupe tous les symboles. Une stratégie excellente sur un symbole et perdante sur un autre obtient un score moyen. Il faut choisir entre ce modèle et un score par (marché, symbole, stratégie).
- **Combinaisons de critères.** Sortino, Calmar, pénalité de turnover…
- **Ajustement des paramètres** sur le segment train (`backtest.Builder`).
- **Redimensionnement** des positions conservées entre deux cycles.
- **Signature** des binaires macOS et Windows : il faut des certificats payants.
- **Édition des stratégies** depuis l'interface. Aujourd'hui, elle se fait dans `config.json`.
- **Vrai bot sur réseau de test** (en partie fait). Portefeuille devnet dédié, airdrop, approbation par entrée et registre au prix réel sont en place. Il reste à exécuter de vrais swaps on-chain, ce qui demande un DEX devnet avec de la liquidité, ou le mainnet avec de petits montants.
- **Algorithme de risque et de gains à l'échelle de la seconde.** Sur 2 h de bougies de 1 s, les deux stratégies du catalogue sont rejetées : `zscore_revert` perd, plombé par les frais, et `sma_cross` ne trade pas. Avec 10 bps de frais et 5 bps de slippage par côté, un aller-retour coûte environ 0,3 %. Une stratégie à quelques dizaines de secondes doit viser des mouvements plus grands que ça.
