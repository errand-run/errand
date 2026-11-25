# Services d'errand, une définition = un port : tour 1

> Laisse **Réponse** vide pour accepter la recommandation. `?` pour plus de détails.
> Écris autre chose (réponse, objection, commentaire) sous n'importe quelle question, puis Envoyer (ou enregistre et dis « ok » dans le chat).

Ce que j'ai vérifié avant d'écrire, pour que les questions partent des faits :

- `services.py` n'existe que sur **lmo**, avec **un seul** service (ollama). Il n'y en a pas sur le Mac.
- Les seuls autres services sont ceux des pages (sdot), et ils sont décrits par les paquets, pas par `services.py`.
- Aujourd'hui le port annoncé **se ferme** quand le serveur s'arrête, et l'agent part après 15 min sans rien à faire.
- Le numéro de port est choisi par le système, dans la plage qu'il redonne aussi aux autres programmes (49152 et plus sur le Mac, 32768–60999 sur lmo).
- Boucles est en Node dans un conteneur sur maison. Elle joint `http://lmo.internal:11434`, qu'un second conteneur relaie par ssh ; la clé de ce relais ne permet que `errand serve connect ollama`.

## Q1 : Le contrat, un bail

Proposition : demander un service rend un **bail**, `{ port, jusqu'à, redemander dans }`. La garantie est simple : jusqu'à la fin du bail, ce port est écouté et mène à **ce** service ou à rien, jamais à un autre. Une connexion qui arrive pendant que le serveur dort le démarre, et attend.

Pour que ce soit vrai, l'agent reste en vie et écoute tant qu'un bail court. Le serveur, lui, s'arrête comme d'habitude (`idle`, ou s'il doit rendre la carte) : seul le port reste. Une connexion ouverte ou une nouvelle demande prolonge le bail.

➡️ **Recommandé :** oui, un bail avec cette garantie.

**Réponse :**

---

## Q2 : Durée du bail

C'est ce qui remplace les « 14 jours ». Trois durées différentes :

- **le bail** (le port est écouté) : 1 jour après la dernière demande ou connexion. Sur une machine partagée (cluster) : 2 h, comme la limite actuelle des services ;
- **le délai de re-demande** (ce que la bibliothèque respecte), fixé par errand dans sa réponse : 10 min ;
- **le numéro réservé** après la fin du bail (Q3).

L'appelant peut demander un bail plus long (`--for 7d`). Un agent qui attend coûte quelques dizaines de Mo.

➡️ **Recommandé :** 1 jour, 2 h sur machine partagée, re-demande toutes les 10 min, `--for` pour plus long.

**Réponse :**

---

## Q3 : Le numéro après le bail

Le numéro reste réservé à cette définition 14 jours après sa dernière utilisation, dans un fichier : redemander rend le même. errand ne le donne à aucun autre service. Il est pris **hors** de la plage du système (par exemple 20000–29999, où est déjà le port de la page), pour qu'un autre programme ne le prenne pas par hasard. S'il est pris quand même, errand en donne un autre et le dit.

➡️ **Recommandé :** oui, 14 jours, plage 20000–29999.

**Réponse :**

---

## Q4 : La bibliothèque redemande, à chaque requête, depuis un cache

Comme tu le proposes : le client appelle `service.url()` avant **chaque** requête. La bibliothèque répond de mémoire, et ne redemande à errand que :

1. quand le délai donné par errand est passé ;
2. quand une connexion est **refusée** : elle redemande une fois et recommence.

Le cas 2 couvre une machine redémarrée ou un agent mort. Le cache est en mémoire seulement, jamais sur disque : il disparaît avec le programme, donc un vieux numéro ne survit pas.

```python
ollama = errand.serve( "ollama.toml", env = "lmo" )       # ne démarre rien
requests.post( ollama.url( "/api/chat" ), … )             # bail pris, ou gardé
```

La contrainte : passer par `url()` au lieu de garder une chaîne. Qui ne le fait pas a quand même 1 jour devant lui (Q2).

➡️ **Recommandé :** oui, ces deux cas, cache en mémoire.

**Réponse :**

---

## Q5 : `services.py`

Je ne vois plus à quoi il sert non plus. Ce qu'il apportait :

| | remplacé par |
|---|---|
| taper `ollama` au lieu de tout | un fichier de définition, qu'on nomme (`errand serve ollama.toml`) |
| partager une définition entre deux clients | le même fichier : git, un paquet, une copie |
| des chemins propres à la machine (`OLLAMA_MODELS` sur lmo) | écrits dans la définition, ou dans les variables de l'environnement errand (`--env lmo`) |
| des sondes en Python | des données (Q9) |
| `dev_sources` (checkout d'un paquet) | reste à placer : `errand-envs.py` ou la configuration de la machine, vu au tour 2 |

La définition devient donc un fichier de **données** (TOML), du côté de celui qui demande. Python n'est plus lancé pour lire des déclarations sur la machine du serveur.

➡️ **Recommandé :** supprimer `services.py`. Une définition est un fichier de données, des options de la ligne de commande, ou un objet de la bibliothèque : les trois donnent la même chose.

**Réponse :**

---

## Q6 : Les services des pages (sdot)

Une page ne doit jamais pouvoir envoyer une commande (ADR 0026) : elle ne donne que `nom#version`. Il faut donc, pour elles seules, que la machine garde une liste de ce qui y est autorisé.

Proposition : elles gardent leur fichier actuel (`services-installed.json`), écrit par `errand serve install`. Il donne la définition complète ; à partir de là, c'est le même mécanisme de bail.

➡️ **Recommandé :** oui, ce fichier reste, réservé aux pages.

**Réponse :**

---

## Q7 : Ce qui fait la définition (l'identité)

Dedans : la machine où il tourne, la commande, les variables **écrites**, le répertoire, les besoins (`gpus`, `ram`…), les noms des ports, les sondes, `version_of` (contenu de fichiers), `version`.

Pas dedans : le numéro demandé, `idle`, `grace`, l'étiquette. Si deux clients diffèrent là-dessus, le plus long gagne.

Jamais pris en compte : l'environnement du terminal. Sinon deux terminaux feraient deux services.

La définition est normalisée avant d'être comparée (ordre des variables, `10G` et `10240M`).

➡️ **Recommandé :** oui.

**Réponse :**

---

## Q8 : Le programme qui change

Ollama est mis à jour : la commande est la même, le programme non. Proposition : le **port** suit la définition écrite, le **serveur** suit la définition et le programme trouvé (chemin, taille, date). À la prochaine connexion, un nouveau serveur démarre derrière le même port ; l'ancien garde ses connexions et finit de lui-même.

L'autre choix : rien de spécial, le nouveau programme sert au prochain démarrage seulement.

➡️ **Recommandé :** port = définition, serveur = définition + programme.

**Réponse :**

---

## Q9 : Les sondes, en données

Une fonction Python ne peut pas être envoyée à une autre machine ni comparée sûrement. Toutes les sondes deviennent des données : une ligne de shell, ou une requête HTTP et un champ. L'ollama de lmo s'écrirait :

```toml
busy = { http = "/api/ps", field = "models" }     # occupé si non vide
```

Le processus Python des sondes disparaît. Une sonde vraiment compliquée devient un script, lancé par la ligne de shell.

➡️ **Recommandé :** oui, plus de fonctions.

**Réponse :**

---

## Q10 : Deux clients, le même gros Ollama

Tu as raison : s'ils demandent la même définition, c'est le même serveur, et il n'y a plus de relais de la carte. La condition est que les définitions soient **égales**. Deux projets qui écrivent chacun la leur auront tôt ou tard `OLLAMA_KEEP_ALIVE` à 1 min d'un côté et 5 min de l'autre, donc deux serveurs de 10 Go.

Deux aides possibles :

- **(a)** une définition partagée : un fichier commun, ou un petit paquet que les deux importent ;
- **(b)** errand le dit : quand un service est demandé alors qu'un autre de même étiquette tourne, la réponse et la page montrent **ce qui diffère** (« OLLAMA_KEEP_ALIVE : 1m ≠ 5m »).

Ce qui se règle par requête (modèle, `num_ctx`, `keep_alive` dans le corps de la requête Ollama) n'a pas à être dans la définition.

➡️ **Recommandé :** (a) par simple fichier, et (b). Pas de compatibilité partielle calculée par errand.

**Réponse :**

---

## Q11 : Où est le port qu'on reçoit

Je propose que le port rendu soit toujours sur **la machine de celui qui demande** (127.0.0.1), où que tourne le serveur. Si c'est lmo, l'agent local relaie chaque connexion par sa liaison ; le client ne voit aucune différence. `errand serve forward` devient inutile : c'est le cas normal.

Le bail est alors tenu par l'agent local, et l'agent de lmo ne garde que le serveur. Si la liaison tombe, une connexion échoue, et la suivante la rétablit.

➡️ **Recommandé :** toujours un port local ; `forward` disparaît.

**Réponse :**

---

## Q12 : Les clients sans errand (Boucles)

Boucles n'a pas errand dans son conteneur, et sa clé ssh ne doit permettre qu'un seul service. Trois voies :

- **(a)** comme aujourd'hui, sans port : la commande forcée de la clé sur lmo devient `errand serve connect /chemin/ollama.toml`. La définition est côté lmo, dans un fichier que Boucles ne peut pas changer, et socat reste ;
- **(b)** le conteneur embarque errand, avec une clé complète sur lmo : il pourrait demander n'importe quelle commande ;
- **(c)** errand tourne sur maison et donne un port au conteneur : la même clé complète, mais sur maison.

➡️ **Recommandé :** (a). `connect` reste le moyen « sans port », et la clé continue de n'ouvrir qu'un service.

**Réponse :**

---

## Q13 : Le verbe, et le démarrage

`errand serve` existe et renvoie déjà une adresse. Proposition : il reste le seul verbe, et par défaut il **ne démarre plus** le serveur, il prend le bail et rend l'adresse. Le serveur démarre à la première connexion ; `--wait` attend qu'il réponde, `-f` reste comme aujourd'hui.

Le prix : la première requête attend le démarrage, et une erreur (commande absente) ne se voit qu'à ce moment. Pour l'erreur la plus fréquente, la demande vérifie tout de suite que le programme existe et que les besoins tiennent sur la machine.

➡️ **Recommandé :** oui, bail seulement par défaut, avec ces deux vérifications.

**Réponse :**

---

## Q14 : Le nom

Le nom n'identifie plus rien. Il devient une **étiquette** pour `status`, `stop` et la page (défaut : le nom du fichier, sinon le premier mot de la commande). `stop ollama` arrête tous ceux qui la portent, `stop ollama~3f2a` un seul.

➡️ **Recommandé :** oui.

**Réponse :**

---

## Q15 : Un numéro imposé

`ports = 11435` existe aujourd'hui, pour une adresse qu'on a mise ailleurs (un marque-page, une configuration). On garde la possibilité de demander un numéro précis ? Il ne compterait pas dans l'identité, et s'il est tenu par une autre définition ou un autre programme, la demande **échoue** en disant par qui, au lieu d'en donner un autre.

➡️ **Recommandé :** oui, en option, avec l'échec net.

**Réponse :**

---

## Q16 : Quelles bibliothèques, et comment elles parlent à errand

Proposition : une seule porte, `errand serve --json …`, qui répond `{ "url", "ports", "until", "again_in" }`. Les bibliothèques sont de minces appels de ce programme : Python dans `errand-lib`, puis Node, une cinquantaine de lignes chacune. N'importe quel langage peut faire pareil sans bibliothèque.

Un appel de programme par re-demande, donc un toutes les 10 min : le coût est négligeable.

➡️ **Recommandé :** la porte est le programme ; Python d'abord, Node quand un client en a besoin (Boucles n'en a pas besoin avec Q12 a).

**Réponse :**

---

## Q17 : L'ADR

L'ADR 0040 est en `proposed`, rien n'en est codé. Je propose de la **remplacer** par une nouvelle version au même numéro (coexistence, identité par contenu et absence de succession restent), plutôt que d'empiler une 0041 qui en annule la moitié.

➡️ **Recommandé :** réécrire 0040 à la fin du grill.

**Réponse :**

---
