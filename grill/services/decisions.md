# Services, une définition = un port : décisions

## Tour 1 (2026-11-25)

- **Contrat** : demander un service rend un bail ; jusqu'à sa fin, le port est écouté et mène à ce service ou à rien. Une connexion qui trouve le serveur arrêté le démarre, et attend.
- **Durée du bail** : donnée par la demande, selon la fréquence et le profil d'usage du demandeur (« on pourra toujours demander large »). Pas de durée fixe.
- **Numéro** : réservé à la définition 14 jours après sa dernière utilisation, dans 20000–29999.
- **Bibliothèque** : `url()` avant chaque requête, réponse de mémoire, redemande au délai donné ou sur connexion refusée. S'il n'y a pas d'agent, il faut pouvoir en réveiller un.
- **Arguments explicites** : par défaut tout passe par des arguments, sérialisés pour la demande. Pas de fichier de définition.
- **`services.py`** : supprimé.
- **Services des pages** : question d'autorisation plutôt que d'installation ; `authorized-services.json`, suivi dans l'interface web. La version pourrait être le hash de la définition.
- **Identité** : machine, commande, variables écrites, répertoire, besoins, noms des ports, sondes, `version_of`, `version` ; normalisée. Pas `idle`, `grace`, étiquette, numéro. Jamais l'environnement du terminal.
- **Programme qui change** : le port suit la définition, le serveur suit la définition et le programme trouvé.
- **Sondes** : des données, plus de fonctions Python.
- **Deux clients du même gros serveur** : définitions égales, sinon deux serveurs. Ni fichier commun ni comparaison faite par errand (« on va se débrouiller »).
- **Port rendu** : toujours sur la machine de celui qui demande ; `forward` disparaît. À voir : sécurité pour Boucles.
- **Verbe** : `errand serve`, qui prend le bail sans démarrer ; programme et besoins vérifiés tout de suite ; `--wait`, `-f`.
- **Nom** : une étiquette.
- **Numéro imposé** : possible, hors identité, échec net s'il est pris.
- **Bibliothèques** : la porte est `errand serve --json` ; Python d'abord, Node quand un client en a besoin.
- **ADR** : 0040 réécrite à la fin du grill.

## Tour 2 (2026-11-25)

Toutes les recommandations acceptées.

- **Partage entre machines** : pas de « run partagé » ; l'agent de la machine qui sert possède le serveur (définition → serveur), par compte. Chaque machine qui demande a son port et son bail, chez son agent.
- **Bail** : un paramètre `keep`, défaut 1 jour ; redemande à la moitié ; la fin la plus tardive l'emporte ; plafond de 2 h sur machine partagée, la réponse donne la vraie fin.
- **`idle`, `grace`** : le plus long des baux en cours.
- **Réveil** : la bibliothèque et `errand serve` démarrent un agent, qui reprend les baux et rouvre les mêmes ports ; `--always` (launchd, systemd) pour un client qui n'a que le port, jamais par défaut, jamais sur un cluster.
- **Arguments** : `errand.serve( [ … ], on = , vars = , gpus = , busy = , idle = , keep = , label = )`, mêmes noms sur la ligne de commande ; rien avant le premier `url()`.
- **Couches** : `on` est une machine ou une liste de couches écrites (`Ssh`, `Vars`, `Module`, `Venv`, `Uv`, `Micromamba` / `Conda`) ; conteneurs et Slurm plus tard.
- **Environnement de base** : le shell de connexion de la machine, pas celui de l'agent.
- **Autorisations** : un seul `~/.config/errand/authorized-services.json`, pour les pages et les clés restreintes ; définition en entier ; un demandeur restreint ne donne qu'un hash.
- **Pages** : hash portable dans la page, hash complet pour le serveur.
- **Interface** : voir, retirer et autoriser dans la page, commande complète dans la confirmation.
- **Profil d'une clé** : `errand there --only key:NOM`, liste blanche côté serveur ; le client n'envoie pas son build.
- **Boucles** : d'abord socat et `errand serve connect HASH` ; errand dans le conteneur ensuite.
- **`--listen`** : explicite, hors identité, refusé sur machine partagée.
- **`dev_sources`, `sources`** : en données dans `~/.config/errand/config.json`.
- **Agent mort ou d'une autre version** : baux sur disque, écoutes rendues puis reprises.
- **Page** : baux, serveur derrière chacun, définition lisible, bouton pour rendre un bail.

## Tour 3 (2026-11-25)

- **Options de connexion** (`others`, `--listen`, numéro imposé, `hold_end`) : hors identité du serveur, mais dans celle du port ; deux demandes qui diffèrent ont deux ports, vers le même serveur.
- **`stop` et `release`** : `stop` arrête le serveur, le port reste ; `release` rend le bail ; `--now` fait les deux.
- **Échec au démarrage** : connexion fermée, raison dans le journal et la page ; la bibliothèque demande l'état et lève l'erreur ; nouvel essai après 2 s, doublé jusqu'à 1 min.
- **Demandeur restreint** : l'autorisation fixe tout, `keep` et `idle` compris (`errand serve authorize --to … -- …`).
- **Services de projet** : `errand.services` et `errand.Service` ne sont plus lus nulle part, et le disent.
- **Passage** : pas de rétrocompatibilité ; Boucles peut être arrêtée le temps du développement, et redémarrée une fois tout mis à jour. Donc `connect` par hash seulement, pas par étiquette.
- **Secrets** : fichiers en 0600 ; valeurs masquées dans la page et `status` si le nom contient `TOKEN`, `KEY`, `SECRET` ou `PASSWORD`.
- **Ordre** : (1) ADR, bail local, page ; (2) définition, hash, environnement de base, sondes, machine distante, `services.py` supprimé ; (3) `errand-lib` Python ; (4) autorisations, pages, `there --only`, `connect` par hash ; (5) Boucles, `--listen`, `--always`, Node.

## À savoir, sans décision de plus

- Entre les étapes 2 et 4, `services-installed.json` reste lu pour les pages (sdot) : `authorized-services.json` le remplace à l'étape 4.
- Non couvert : Windows, conteneurs, Slurm (un port sur un nœud).
- ADR amendées : 0018, 0026, 0027, 0028. 0040 est réécrite.
