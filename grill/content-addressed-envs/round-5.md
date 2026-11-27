# Environnements adressés par leur contenu : round 5, errand fait les téléchargements

> Laisse **Réponse** vide pour accepter la recommandation. `?` demande plus de détails.
> Écris n'importe quoi d'autre (réponse, objection, commentaire) sous une question, puis Envoyer.

Ta réponse au round 4 : errand se charge de **tous** les téléchargements, les range dans le cache comme non précieux, et les demande à
un agent parent quand il n'y a pas internet.

Ce que les ADR disent déjà ( à respecter ) :

- **ADR 0013** : `errand.download( url )` range un fichier sous `downloads/<sha256>/`, et sans internet la demande **remonte la chaîne
  des agents**. Mais c'est la zone `/errand-cache` **du projet** ( synchronisée ), faite pour les données d'entrée.
- **ADR 0035, 2026-11-24** : l'internet se demande par **une requête qui attend** ( un fichier ), **jamais par un lien gardé ouvert** entre
  le nœud et la frontale ( « beaucoup trop coûteux » ). L'agent de la frontale est supposé rester. Et pour `pip install` sans sortie :
  « je préfère que ça se fasse sur la machine qui le demande ( … ) quitte à y faire plein d'étapes », laissé ouvert.

Ce round referme cette question ouverte de l'ADR 0035.

---

## Q1 : Le mécanisme, un proxy local qui pose des requêtes qui attendent

Les outils ( micromamba, uv, apptainer ) parlent HTTP, pas « fichier ». Proposition :

- **L'agent de la place** ( celui de la machine, ou du job sous Slurm ) sert un **proxy HTTP local** ( `127.0.0.1` ). Un outil qu'errand
  lance y est dirigé.
- Une requête : **dans le cache** → servie tout de suite. **Pas dans le cache, internet ici** → téléchargée, rangée, servie.
  **Pas d'internet** → l'agent pose une **requête qui attend** ( le mécanisme de l'ADR 0035 ), la réponse HTTP reste suspendue jusqu'à ce
  qu'un agent qui a internet l'ait remplie, puis elle est servie.
- Le seul lien vivant est **local** ( l'outil et son proxy, sur la même machine ) : rien n'est ouvert entre le nœud et la frontale.
- La résolution reste **sur la machine qui construit** ( round 2 ) : les index ( `repodata.json`, pages de PyPI ) passent par le même
  chemin que les paquets.
- Les délais des outils sont relevés pendant ces commandes ( `UV_HTTP_TIMEOUT`, `remote_read_timeout_secs` de micromamba ) : une requête
  relayée peut prendre des minutes.

➡️ **Recommandé :** oui, un proxy local servi par l'agent, qui pose des requêtes qui attendent quand il n'a pas internet.

**Réponse :**

---

## Q2 : Comment les outils passent par le proxy

En HTTPS, un proxy classique ( `HTTPS_PROXY`, tunnel `CONNECT` ) ne voit que du chiffré : il ne peut rien mettre en cache, et le tunnel
est un lien vivant. Deux façons d'y voir clair :

- **(a) Réécrire les URLs des outils qu'errand pilote** : micromamba reçoit ses canaux sous la forme
  `http://127.0.0.1:P/https/conda.anaconda.org/conda-forge` ; uv reçoit `--index-url http://127.0.0.1:P/https/pypi.org/simple`, et le
  proxy réécrit dans les pages de l'index les liens vers `files.pythonhosted.org` ; apptainer reçoit un miroir de registre pour
  `docker://`. Ce qu'errand ne pilote pas ( un `curl` dans un `%post` ) n'est pas couvert, mais `errand download` l'est.
- **(b) Un proxy qui déchiffre** ( errand fabrique une autorité de certification et la donne aux outils par `SSL_CERT_FILE`,
  `REQUESTS_CA_BUNDLE` … ) : couvre tout, mais c'est un intermédiaire TLS sur ta machine, chaque outil lit ses certificats à sa façon, et
  un oubli casse en silence.

➡️ **Recommandé :** (a), réécrire les URLs des outils pilotés ; pas d'interception TLS.

**Réponse :**

---

## Q3 : Le cache des téléchargements

- **Par machine** ( par système de fichiers ), à côté du store des envs : `<cache>/downloads/<sha256>/<nom>`, au format des caches
  d'errand ( ADR 0020 ), donc vu et retiré par `errand caches`.
- **Non précieux** : un fichier qui n'a pas servi depuis **14 jours** part tout seul ( comme les intermédiaires de l'ADR 0039 ) ; il se
  retélécharge.
- Un fichier **immuable** ( un paquet conda, une roue : vérifiés par md5 / sha256 ) est servi du cache sans rien demander. Un index
  **changeant** ( `repodata.json`, une page de PyPI ) est revalidé après **1 heure** ; sans internet nulle part sur la chaîne, l'ancien
  est servi, et la résolution le dit.
- `errand.download` de l'ADR 0013 ( les données d'entrée d'un projet, dans sa zone synchronisée ) ne change pas : ce sont des données du
  projet, pas des outils.

➡️ **Recommandé :** oui, cache par machine, non précieux à 14 jours, index revalidés après 1 heure.

**Réponse :**

---

## Q4 : Le relais

Quand Jean Zay demande un paquet, la requête remonte ( frontale, puis ton portable ) jusqu'au premier agent qui a internet.

- **Chaque agent qui relaie garde une copie** dans son cache ( non précieuse ) : la deuxième machine qui le demande le trouve plus près.
- Le fichier descend par le transport de fichiers des agents ( ADR 0006 ), pas par un nouveau canal.
- Qui a internet : **constaté** ( un essai à délai court, gardé quelques minutes ), et **déclarable** sur le `Ssh` ( `internet = False` )
  pour ne même pas essayer.

➡️ **Recommandé :** oui, copie à chaque relais, transport existant, internet constaté et déclarable.

**Réponse :**

---

## Q5 : Un ADR à part

Ce mécanisme sert bien au-delà des envs : les images, `errand.download`, plus tard `pip` dans un script. Proposition : **un ADR à part,
« errand fait les téléchargements »**, qui amende l'ADR 0013 et ferme la question ouverte de l'ADR 0035. L'ADR des envs adressés par
contenu s'appuie dessus pour les machines sans internet ; les deux s'écrivent ensemble, et l'implémentation commence par les envs
sur une machine connectée.

➡️ **Recommandé :** oui, deux ADR écrits ensemble, l'implémentation par les envs d'abord.

**Réponse :**

---
