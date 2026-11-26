# Services d'errand, une définition = un port : tour 2

> Laisse **Réponse** vide pour accepter la recommandation. `?` pour plus de détails.
> Écris autre chose sous n'importe quelle question, puis Envoyer.

Les décisions du tour 1 sont dans `grill/services/decisions.md`. Faits vérifiés pour ce tour :

- Un agent distant se joint par `ssh … errand there` ; si l'errand de là-bas est d'un autre build, le Mac **y envoie le sien**. Une clé à commande forcée ne peut pas le permettre.
- Une fois la liaison ouverte, un agent accepte **toutes** les opérations (lancer, lire un fichier, supprimer des runs, passer à une autre machine…) : il n'existe aucun profil.
- Un service est lancé avec les variables **de l'agent**, donc selon la façon dont l'agent a démarré (terminal, ssh…). Les couches autres que `Ssh` (conda, conteneur) sont ignorées.
- La page d'errand vérifie le cookie et l'origine de chaque action ; les vues sont servies sur un autre port, donc une autre origine.

## Q1 : Ollama de lmo, partagé entre deux machines (ta question de Q11)

Oui, mais ce n'est pas un nouveau type de run : c'est déjà ce que fait l'agent de lmo, qui **possède** le serveur. Il garde une table définition → serveur. Le Mac et maison lui demandent la même définition, donc reçoivent le même serveur.

| | tenu par |
|---|---|
| le port local, son bail | l'agent de chaque machine qui demande |
| le serveur, sa place dans la file, le compte des connexions | l'agent de lmo, pour tous |

Le serveur est « en usage » tant qu'**une** connexion est ouverte, d'où qu'elle vienne. Le partage est par compte de lmo : deux comptes différents ont deux serveurs. L'agent de lmo n'a pas à rester en vie pour les baux : l'agent local le réveille par ssh à la connexion suivante.

➡️ **Recommandé :** oui. Pas de notion de run partagé ; le serveur appartient à l'agent de sa machine.

**Réponse :**

---

## Q2 : Écrire la durée du bail

Le bail vient de la demande. Je propose un seul paramètre, `keep` : combien de temps le port reste ouvert après la dernière demande ou connexion (`keep = "2h"`, `"7d"`).

- Dans la bibliothèque et sur la ligne de commande, il a un défaut pour qui ne veut pas y penser : **1 jour**.
- Le délai avant de redemander est déduit : la moitié du bail.
- Deux demandes de la même définition sur une machine : le port reste jusqu'à la plus tardive des deux fins.
- Sur une machine partagée (cluster), errand plafonne à 2 h ; la réponse donne la vraie fin.

➡️ **Recommandé :** oui, `keep`, défaut 1 jour.

**Réponse :**

---

## Q3 : `idle` et `grace`

`idle` (le serveur s'arrête après tant de temps sans usage) est aussi une affaire de demande. Le plus long des baux **en cours** l'emporte : quand le client qui voulait 1 h s'en va (bail fini), on retombe sur les 2 min de l'autre. Pour un serveur distant, la durée est portée par chaque connexion et chaque demande ; elle ne dépasse pas le bail de celui qui l'a demandée.

➡️ **Recommandé :** oui, le plus long des baux en cours.

**Réponse :**

---

## Q4 : Réveiller un agent

Trois cas où le port ne répond pas (redémarrage, agent tué) :

- **la bibliothèque ou `errand serve`** : ils démarrent un agent, qui reprend les baux écrits sur disque et rouvre les mêmes ports. Rien à faire de plus ;
- **un client qui ne connaît que le port** (navigateur, curl) : rien ne peut réveiller, sauf si le système écoute à la place d'errand. C'est `--keep` de `forward` aujourd'hui (launchd, systemd), sur demande explicite ;
- **un cluster** : jamais d'écoute par le système.

Je reprendrais le deuxième sous le nom `--always` : le système garde le port, errand ne tourne pas entre deux usages.

➡️ **Recommandé :** oui ; l'option `--always`, jamais par défaut.

**Réponse :**

---

## Q5 : Les arguments explicites

Plus de fichier. En Python :

```python
ollama = errand.serve(
    [ "ollama", "serve" ],
    on    = "lmo",                                            # une machine : nom ssh ou du registre ; absent : ici
    vars  = { "OLLAMA_HOST": "127.0.0.1:{port}", "OLLAMA_KEEP_ALIVE": "1m" },
    gpus  = 1, gpu_ram = "10G",
    busy  = errand.http( "/api/ps", field = "models" ),
    idle  = "2m", keep = "1d", label = "ollama" )

requests.post( ollama.url( "/api/chat" ), json = … )
```

et la ligne de commande a les mêmes noms : `errand serve --on lmo --var OLLAMA_KEEP_ALIVE=1m --gpus 1 … -- ollama serve`. `serve()` est instantané : il ne parle à errand qu'au premier `url()`.

`env = "lmo"` dans mon exemple précédent voulait dire l'environnement `lmo` déclaré dans un `errand-envs.py`, donc une définition cachée ailleurs. Je la remplace par `on`.

➡️ **Recommandé :** cette forme.

**Réponse :**

---

## Q6 : Les couches (conda, conteneur)

Un serveur Python a souvent besoin d'un environnement. `on` accepte une machine, ou une liste de couches écrites en entier, les mêmes qu'un run :

```python
on = [ errand.Ssh( "lmo" ), errand.Micromamba( "jax" ) ]
```

Elles sont sérialisées dans la définition (donc dans l'identité). Sans conteneur ni Slurm pour commencer : le port d'un serveur dans un conteneur ou sur un nœud est une autre question.

➡️ **Recommandé :** oui, `Ssh`, `Vars`, `Module`, `Venv`, `Uv`, `Micromamba` / `Conda` ; le reste plus tard.

**Réponse :**

---

## Q7 : Les variables de départ

Aujourd'hui, le serveur hérite de ce que l'agent avait en démarrant : le même service peut trouver un autre `ollama` selon que l'agent vient d'un terminal ou d'un ssh. Proposition : un environnement de base fixe, celui d'un shell de connexion de la machine (relu par l'agent, pas hérité), plus les couches, plus les variables écrites. Le programme trouvé entre dans l'empreinte du serveur (Q8 du tour 1).

➡️ **Recommandé :** oui, base = shell de connexion.

**Réponse :**

---

## Q8 : Une seule liste d'autorisations

Deux demandeurs ne doivent pas pouvoir demander n'importe quoi : une **page** (Q6 du tour 1) et une **clé restreinte** (Boucles). C'est le même besoin. Un seul fichier sur la machine qui sert, `~/.config/errand/authorized-services.json` :

```json
{ "3f2a91c0": { "label": "ollama", "definition": { … en entier … }, "to": [ "key:boucles" ], "since": "2026-11-25" },
  "63ecc287": { "label": "blender", "definition": { … }, "to": [ "pages" ] } }
```

Un demandeur restreint ne donne qu'un **hash** ; il n'envoie jamais de définition. La définition autorisée est écrite en entier, donc lisible. Les demandes faites par toi (ton compte, liaison normale) ne passent pas par cette liste.

➡️ **Recommandé :** un seul fichier pour les pages et les clés.

**Réponse :**

---

## Q9 : Le hash des pages

Pour une page, le producteur (sdot) calcule aujourd'hui `version` à partir des fichiers du paquet, sans chemin de machine : le même hash partout. Le hash de la définition **complète** contient `/Applications/Blender.app/…`, donc change d'une machine à l'autre, et la page ne peut pas le connaître.

Proposition : la page continue de donner le hash **portable** du paquet (ce qui est décrit) ; l'autorisation sur la machine dit « ce hash portable → cette définition complète », et l'identité du serveur est le hash complet. Pour une clé restreinte, le hash est celui de la définition complète.

➡️ **Recommandé :** oui, deux hash : portable dans la page, complet pour le serveur.

**Réponse :**

---

## Q10 : Qui autorise, et où

ADR 0027 : la page montre la ligne, une personne la tape. Tu veux un suivi dans l'interface web. Proposition, dans l'onglet Machines :

- voir les autorisations de chaque machine, par qui, dernier usage ; en **retirer** une ;
- voir les demandes **refusées** récentes (« la page X demande blender 63ecc287 »), avec la commande complète ;
- **autoriser** d'un clic, après confirmation qui affiche la commande en entier.

Un clic autorise à lancer un programme. C'est acceptable parce que la page d'errand vérifie cookie et origine, et que les vues n'y ont pas accès. Sinon, on garde « à taper ».

➡️ **Recommandé :** voir, retirer et autoriser dans la page, avec la commande complète dans la confirmation.

**Réponse :**

---

## Q11 : Le profil d'une clé (ta question de Q12)

Oui, c'est faisable. Sur lmo, la ligne de la clé devient :

```text
restrict,command="~/.cache/errand/bin/errand there --only key:boucles" ssh-ed25519 …
```

C'est un vrai lien d'errand, mais l'agent n'y accepte que `hello`, et les services dont `authorized-services.json` dit `to: key:boucles`. Tout le reste est refusé (lancer, lire, supprimer, passer à une autre machine). Le filtre est dans l'agent de lmo, par liste blanche d'opérations, pas chez le client.

Ce que ça impose :

- le client ne peut pas envoyer son build : il parle à l'errand stable de lmo. Si les protocoles ne s'accordent pas, il le dit clairement au lieu d'essayer ;
- il ne passe pas par un shell interactif, et le saut par `sas` reste dans la configuration ssh.

➡️ **Recommandé :** oui, `there --only`, liste blanche côté serveur.

**Réponse :**

---

## Q12 : Boucles, alors

- **(a)** errand dans le conteneur `tunnel-lmo`, à la place de socat et du relais, avec la clé restreinte de Q11. Boucles (Node) joint toujours `http://lmo.internal:11434`, dont Ollama a besoin dans l'en-tête `Host`.
- **(b)** comme aujourd'hui : socat, et `errand serve connect HASH` en commande forcée. Aucun errand dans le conteneur.

(a) donne le réveil, l'attente et les messages d'errand ; il coûte de construire un errand linux/arm64 dans l'image. (b) marche déjà, au prix d'une commande par connexion. `connect HASH` reste de toute façon utile.

➡️ **Recommandé :** (b) d'abord (une ligne à changer), (a) quand Q11 est codé.

**Réponse :**

---

## Q13 : Écouter ailleurs que sur 127.0.0.1

Dans un conteneur, le voisin n'est pas sur 127.0.0.1. Il faut `--listen 0.0.0.0:11434` pour (a). Proposition : possible, **jamais** par défaut, hors identité, refusé sur une machine partagée. Les ports sur 127.0.0.1 gardent la règle actuelle (les autres comptes sont refusés sous Linux, sauf `others`).

➡️ **Recommandé :** oui, option explicite.

**Réponse :**

---

## Q14 : `dev_sources` et `sources`

`services.py` portait aussi `dev_sources` (tel dépôt est ce checkout) et `sources` (d'où `fetch` peut prendre du code). Proposition : les mettre en données dans `~/.config/errand/config.json` de la machine, modifiables par commande et par la page, comme l'interrupteur `dev_sources_off` l'est déjà.

➡️ **Recommandé :** oui.

**Réponse :**

---

## Q15 : Un agent qui meurt ou qui change de version

Les baux sont sur disque (définition, port, fin, `keep`, `listen`). Un nouvel agent les relit et rouvre les ports. Entre deux versions d'errand : l'ancien ferme ses écoutes et garde ses connexions ouvertes, le nouveau les rouvre ; une connexion qui arrive dans l'instant est refusée, et la bibliothèque redemande. Les **serveurs** distants restent à l'agent qui les a, comme aujourd'hui.

➡️ **Recommandé :** oui.

**Réponse :**

---

## Q16 : Ce que la page montre

Avec ce que je viens de coder (serveurs tenus et arrêtés), il faudrait par machine : les baux (étiquette, port, fin, dernier usage, qui a demandé), le serveur derrière chacun, la définition lisible en entier, et un bouton pour **rendre** un bail. `errand status` en dirait le nombre.

➡️ **Recommandé :** oui.

**Réponse :**

---
