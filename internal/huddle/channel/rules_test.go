package channel

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

func TestRules(t *testing.T) {
	t.Run("approval rules: what each matches, and what it leaves alone", func(t *testing.T) {
		ids := func(c string) []string {
			out := []string{}
			for _, r := range Matching(c) {
				out = append(out, r.ID)
			}
			slices.Sort(out)
			return out
		}
		for _, c := range []struct {
			cmd  string
			want []string
		}{
			{"git push --force origin main", []string{"force_push", "git_push"}},
			{"git push -f", []string{"force_push", "git_push"}},
			{"git push --force-with-lease", []string{"force_push", "git_push"}},
			{"git push origin +main", []string{"force_push", "git_push"}},
			{"cd repo && git -C x push origin main", []string{"git_push"}},
			{"git push origin --delete old", []string{"delete", "git_push"}},
			{"git push origin :old", []string{"delete", "git_push"}},
			{"rm -rf build", []string{"delete"}},
			{"ls && rm x.txt", []string{"delete"}},
			{"git rm -r old/", []string{"delete"}},
			{"git branch -D feature", []string{"delete"}},
			{"git clean -fdx", []string{"delete"}},
			{"find . -name '*.o' -delete", []string{"delete"}},
			{"git tag v1.2.0", []string{"git_tag"}},
			{"git tag -a v1 -m x", []string{"git_tag"}},
			{"npm publish --access public", []string{"publish"}},
			{"cargo publish", []string{"publish"}},
			{"python -m twine upload dist/*", []string{"publish"}},
			{"docker push me/app:1", []string{"publish"}},
			{"gh release create v1", []string{"publish"}},
		} {
			if got := ids(c.cmd); !slices.Equal(got, c.want) {
				t.Errorf("matching(%q) = %v, want %v", c.cmd, got, c.want)
			}
		}
		for _, c := range []string{
			"git status", "git log --oneline", "git tag", "git tag -l", "git tag --list 'v*'", "npm run build",
			"echo pushing", "git branch -a", "npm run rm-dist", "ls -la", "bun test", "cat README.md", "", "git push-helper",
		} {
			if got := Matching(c); len(got) != 0 {
				t.Errorf("matching(%q) = %v, want none", c, got)
			}
		}
		want := []Switch{{ID: "force_push", On: true}, {ID: "delete", On: true}, {ID: "git_push", On: false}, {ID: "git_tag", On: false}, {ID: "publish", On: false}}
		if got := Defaults(); !slices.Equal(got, want) {
			t.Errorf("defaults = %v, want %v", got, want)
		}
		got := Effective(vv(t, `{"git_push": true, "bogus": true, "delete": "no"}`))
		wantEff := []Switch{{ID: "force_push", On: true}, {ID: "delete", On: true}, {ID: "git_push", On: true}, {ID: "git_tag", On: false}, {ID: "publish", On: false}}
		if !slices.Equal(got, wantEff) {
			t.Errorf("effective = %v, want %v", got, wantEff)
		}
	})

	t.Run("the command word sits at a command position, after git options, and ends cleanly", func(t *testing.T) {
		for _, c := range []struct {
			cmd  string
			want []string
		}{
			{"git push", []string{"git_push"}},
			{"sudo git push", []string{"git_push"}},
			{"make && git push", []string{"git_push"}},
			{"(git push)", []string{"git_push"}},
			{"echo $(git tag 1.0)", []string{"git_tag"}},
			{"then git rm x", []string{"delete"}},
			{"exec rm x", []string{"delete"}},
			{"git -c a.b=c push", []string{"git_push"}},
			{"git --no-pager branch --delete x", []string{"delete"}},
			{"git push --force-with-lease=ref", []string{"force_push", "git_push"}},
			{"twine upload dist/*", []string{"publish"}},
			{"python3 -m twine upload dist/*", []string{"publish"}},
			{"yarn npm publish", []string{"publish"}},
			{"docker image push me/app", []string{"publish"}},
			{"gem push pkg.gem", []string{"publish"}},
			{"git tag --contains v1", nil},
			{"git tag -n99", nil},
			{"git tag -v v1", nil},
			{"rm=x", nil},
			{"rmdir dir", []string{"delete"}},
			{"shred file", []string{"delete"}},
			{"trash file", []string{"delete"}},
			{"unlink file", []string{"delete"}},
			{"pseudo rm x", nil},
			{"mygit push", nil},
		} {
			var got []string
			for _, r := range Matching(c.cmd) {
				got = append(got, r.ID)
			}
			slices.Sort(got)
			if !slices.Equal(got, c.want) {
				t.Errorf("matching(%q) = %v, want %v", c.cmd, got, c.want)
			}
		}
	})
}

// matchingVectors is the matcher's verdict table: 210 commands with the rule ids matching
// answers for each, the padded vectors' padding applied.
func TestMatchingVectors(t *testing.T) {
	for _, v := range []struct {
		id             string
		cmd            string
		padA, padEmoji int
		want           []string
	}{
		{"v001_empty", "", 0, 0, nil},
		{"v002_spaces", "   ", 0, 0, nil},
		{"v003_tab_newline", "\t\n", 0, 0, nil},
		{"v004_semis", ";;;", 0, 0, nil},
		{"v005_amps", "&&&", 0, 0, nil},
		{"v006_gp_force", "git push --force", 0, 0, []string{"force_push", "git_push"}},
		{"v007_gp_f", "git push -f", 0, 0, []string{"force_push", "git_push"}},
		{"v008_gp_fwl", "git push --force-with-lease", 0, 0, []string{"force_push", "git_push"}},
		{"v009_gp_fwl_eq", "git push --force-with-lease=main", 0, 0, []string{"force_push", "git_push"}},
		{"v010_gp_plusref", "git push origin +main", 0, 0, []string{"force_push", "git_push"}},
		{"v011_gp_plusfull", "git push origin +refs/heads/topic:main", 0, 0, []string{"force_push", "git_push"}},
		{"v012_gp_f_first", "git push -f origin main", 0, 0, []string{"force_push", "git_push"}},
		{"v013_gp_force_last", "git push origin main --force", 0, 0, []string{"force_push", "git_push"}},
		{"v014_gp_force_first", "git push --force origin main", 0, 0, []string{"force_push", "git_push"}},
		{"v015_gp_nf", "git push -nf x", 0, 0, []string{"force_push", "git_push"}},
		{"v016_gp_fi", "git push -fi", 0, 0, []string{"force_push", "git_push"}},
		{"v017_gp_if", "git push -if", 0, 0, []string{"force_push", "git_push"}},
		{"v018_gp_fwoutlease", "git push --force-without-lease", 0, 0, []string{"force_push", "git_push"}},
		{"v019_gp_u", "git push -u origin main", 0, 0, []string{"git_push"}},
		{"v020_gp_lead_spaces", "  git push -f", 0, 0, []string{"force_push", "git_push"}},
		{"v021_gp_lead_nbsp", "\u00a0git push -f", 0, 0, []string{"force_push", "git_push"}},
		{"v022_gp_sudo", "sudo git push --force", 0, 0, []string{"force_push", "git_push"}},
		{"v023_gp_exec", "exec git push --force", 0, 0, []string{"force_push", "git_push"}},
		{"v024_gp_then", "if true; then git push -f; fi", 0, 0, []string{"force_push", "git_push"}},
		{"v025_gp_do", "while true; do git push -f; done", 0, 0, []string{"force_push", "git_push"}},
		{"v026_gp_do_nl", "do\ngit push -f", 0, 0, []string{"force_push", "git_push"}},
		{"v027_gp_undo", "undo git push -f", 0, 0, nil},
		{"v028_gp_andand", "cd repo && git push --force", 0, 0, []string{"force_push", "git_push"}},
		{"v029_gp_or", "git push --force || echo skipped", 0, 0, []string{"force_push", "git_push"}},
		{"v030_gp_semi", "git add -A; git push --force", 0, 0, []string{"force_push", "git_push"}},
		{"v031_gp_nl_only", "echo hi\ngit push -f", 0, 0, nil},
		{"v032_gp_time", "time git push -f", 0, 0, nil},
		{"v033_gp_legit", "legit push -f", 0, 0, nil},
		{"v034_gp_pushy", "git pushy -f", 0, 0, nil},
		{"v035_gp_push_helper", "git push-helper --force", 0, 0, nil},
		{"v036_gp_upper", "GIT PUSH --FORCE", 0, 0, nil},
		{"v037_gp_sub_upper", "git PUSH -f", 0, 0, nil},
		{"v038_gp_dashC", "git -C sub push --force", 0, 0, []string{"force_push", "git_push"}},
		{"v039_gp_dashc", "git -c key=val push -f", 0, 0, []string{"force_push", "git_push"}},
		{"v040_gp_worktree", "git --work-tree=/tmp push -f", 0, 0, []string{"force_push", "git_push"}},
		{"v041_gp_nolocks", "git --no-optional-locks push", 0, 0, []string{"git_push"}},
		{"v042_gp_opt_val", "git --foo bar push -f", 0, 0, nil},
		{"v043_gp_opt_eq_val", "git --foo=bar baz push -f", 0, 0, nil},
		{"v044_gp_emoji_suffix", "git push🚀", 0, 0, []string{"git_push"}},
		{"v045_gp_emoji_arg", "git push 🚀 --force", 0, 0, []string{"force_push", "git_push"}},
		{"v046_gp_plus_eol", "git push origin +", 0, 0, []string{"git_push"}},
		{"v047_gp_tab_force", "git push\t--force", 0, 0, []string{"force_push", "git_push"}},
		{"v048_gp_backtick", "`git push -f`", 0, 0, []string{"force_push", "git_push"}},
		{"v049_gp_quoted_sub", "git `push` -f", 0, 0, nil},
		{"v050_gp_cmdsub", "$(git push -f)", 0, 0, []string{"force_push", "git_push"}},
		{"v051_gp_fwl_full", "git push --force-with-lease=refs/heads/main", 0, 0, []string{"force_push", "git_push"}},
		{"v052_gp_quiet", "git push --quiet --force", 0, 0, []string{"force_push", "git_push"}},
		{"v053_gp_qf", "git push -qf", 0, 0, []string{"force_push", "git_push"}},
		{"v054_gp_dashC_nbsp", "git -C\u00a0/sub push -f", 0, 0, []string{"force_push", "git_push"}},
		{"v055_gp_plus_nbsp", "git push origin +\u00a0x", 0, 0, []string{"git_push"}},
		{"v056_gp_force_emoji", "git push --force🚀", 0, 0, []string{"force_push", "git_push"}},
		{"v057_gp_paren", "(git push -f)", 0, 0, []string{"force_push", "git_push"}},
		{"v058_gp_force_eq", "git push --force=x", 0, 0, []string{"force_push", "git_push"}},
		{"v059_gp_forceful", "git push --forceful", 0, 0, []string{"git_push"}},
		{"v060_del_rm_rf", "rm -rf /", 0, 0, []string{"delete"}},
		{"v061_del_rm_file", "rm file.txt", 0, 0, []string{"delete"}},
		{"v062_del_rm_sp_eq", "rm  =x", 0, 0, nil},
		{"v063_del_rm_sp_eq_x", "rm = x", 0, 0, nil},
		{"v064_del_rm_assign", "rm=x", 0, 0, nil},
		{"v065_del_rm_assign_then", "rm=x; rm y", 0, 0, []string{"delete"}},
		{"v066_del_upper_assign", "RM=x trash y", 0, 0, nil},
		{"v067_del_trash", "trash y", 0, 0, []string{"delete"}},
		{"v068_del_shred", "shred file", 0, 0, []string{"delete"}},
		{"v069_del_unlink", "unlink link", 0, 0, []string{"delete"}},
		{"v070_del_rmdir", "rmdir emptydir", 0, 0, []string{"delete"}},
		{"v071_del_rmdir_assign", "rmdir=oops; rmdir ok", 0, 0, []string{"delete"}},
		{"v072_del_rm_nbsp_eq", "rm\u00a0= x", 0, 0, nil},
		{"v073_del_rm_ideo_eq", "rm\u3000= x", 0, 0, nil},
		{"v074_del_rm_ls_eq", "rm\u2028= x", 0, 0, nil},
		{"v075_del_rm_vt_eq", "rm\x0b= x", 0, 0, nil},
		{"v076_del_quoted", "echo 'rm -rf x'", 0, 0, nil},
		{"v077_del_backtick", "`rm -rf x`", 0, 0, []string{"delete"}},
		{"v078_del_cmdsub", "$(rm -rf x)", 0, 0, []string{"delete"}},
		{"v079_del_paren", "(rm x)", 0, 0, []string{"delete"}},
		{"v080_del_paren2", "((rm x))", 0, 0, []string{"delete"}},
		{"v081_del_brace", "{rm x}", 0, 0, nil},
		{"v082_del_xargs", "cat list | xargs rm", 0, 0, nil},
		{"v083_del_pipe", "cat f | rm", 0, 0, []string{"delete"}},
		{"v084_del_semi_nospace", "ls;rm x", 0, 0, []string{"delete"}},
		{"v085_del_sudo", "sudo rm x", 0, 0, []string{"delete"}},
		{"v086_del_sudo_u", "sudo -u root rm x", 0, 0, nil},
		{"v087_del_sudo_nbsp", "sudo\u00a0rm x", 0, 0, []string{"delete"}},
		{"v088_del_sudo_vt", "sudo\x0brm -rf x", 0, 0, []string{"delete"}},
		{"v089_del_semi_vt", ";\x0brm x", 0, 0, []string{"delete"}},
		{"v090_git_vt_push", "git\x0bpush -f", 0, 0, []string{"force_push", "git_push"}},
		{"v091_git_rm", "git rm -rf cached", 0, 0, []string{"delete"}},
		{"v092_git_rm_bare", "git rm", 0, 0, []string{"delete"}},
		{"v093_git_clean_fd", "git clean -fd", 0, 0, []string{"delete"}},
		{"v094_git_clean_n", "git clean -n", 0, 0, []string{"delete"}},
		{"v095_git_clean_fdx", "git clean -fdx", 0, 0, []string{"delete"}},
		{"v096_git_dashC_rm", "git -C sub rm x", 0, 0, []string{"delete"}},
		{"v097_sudo_git_clean", "sudo git clean -fd", 0, 0, []string{"delete"}},
		{"v098_br_d", "git branch -d x", 0, 0, []string{"delete"}},
		{"v099_br_D", "git branch -D x", 0, 0, []string{"delete"}},
		{"v100_br_del_long", "git branch --delete x", 0, 0, []string{"delete"}},
		{"v101_br_ad", "git branch -ad x", 0, 0, []string{"delete"}},
		{"v102_br_mD", "git branch -mD x", 0, 0, []string{"delete"}},
		{"v103_br_del_eq", "git branch --delete=x", 0, 0, []string{"delete"}},
		{"v104_br_plain", "git branch topic", 0, 0, nil},
		{"v105_br_semi_d", "git branch topic; echo -d", 0, 0, nil},
		{"v106_br_d_bare", "git branch -d", 0, 0, []string{"delete"}},
		{"v107_push_del_long", "git push origin --delete branch", 0, 0, []string{"delete", "git_push"}},
		{"v108_push_d", "git push -d origin branch", 0, 0, []string{"delete", "git_push"}},
		{"v109_push_del_bare", "git push --delete branch", 0, 0, []string{"delete", "git_push"}},
		{"v110_push_colon", "git push origin :topic", 0, 0, []string{"delete", "git_push"}},
		{"v111_push_dcolon", "git push origin ::topic", 0, 0, []string{"delete", "git_push"}},
		{"v112_push_colon_eol", "git push origin :", 0, 0, []string{"git_push"}},
		{"v113_push_del_eol", "git push --delete", 0, 0, []string{"delete", "git_push"}},
		{"v114_push_colon_nbsp", "git push origin :\u00a0x", 0, 0, []string{"git_push"}},
		{"v115_find_del", "find . -name '*.tmp' -delete", 0, 0, []string{"delete"}},
		{"v116_find_varlog", "find /var/log -type f -delete", 0, 0, []string{"delete"}},
		{"v117_find_sudo", "sudo find . -delete", 0, 0, []string{"delete"}},
		{"v118_find_deleted", "find . -deleted", 0, 0, nil},
		{"v119_findx", "findx . -delete", 0, 0, nil},
		{"v120_find_nl", "find .\n-delete", 0, 0, []string{"delete"}},
		{"v121_find_tab", "find . -name a\t-delete", 0, 0, []string{"delete"}},
		{"v122_find_nbsp", "find .\u00a0-delete", 0, 0, []string{"delete"}},
		{"v123_find_xargs", "xargs find . -delete", 0, 0, nil},
		{"v124_push_bare", "git push", 0, 0, []string{"git_push"}},
		{"v125_push_origin", "git push origin", 0, 0, []string{"git_push"}},
		{"v126_git_p_push", "git -p push", 0, 0, []string{"git_push"}},
		{"v127_git_exec_push", "git exec push", 0, 0, nil},
		{"v128_push_dryrun", "git push --dry-run", 0, 0, []string{"git_push"}},
		{"v129_push_followtags", "git push --follow-tags", 0, 0, []string{"git_push"}},
		{"v130_push_all", "git push --all", 0, 0, []string{"git_push"}},
		{"v131_push_head", "git push origin HEAD", 0, 0, []string{"git_push"}},
		{"v132_tag_create", "git tag v1.0", 0, 0, []string{"git_tag"}},
		{"v133_tag_bare", "git tag", 0, 0, nil},
		{"v134_tag_l", "git tag -l", 0, 0, nil},
		{"v135_tag_list", "git tag --list", 0, 0, nil},
		{"v136_tag_n", "git tag -n", 0, 0, nil},
		{"v137_tag_n5", "git tag -n5", 0, 0, nil},
		{"v138_tag_n10_x", "git tag -n10 x", 0, 0, nil},
		{"v139_tag_n0", "git tag -n0", 0, 0, nil},
		{"v140_tag_nx", "git tag -nx", 0, 0, []string{"git_tag"}},
		{"v141_tag_v_sp", "git tag -v v1.0", 0, 0, nil},
		{"v142_tag_v_glued", "git tag -v1.0", 0, 0, []string{"git_tag"}},
		{"v143_tag_contains", "git tag --contains HEAD", 0, 0, nil},
		{"v144_tag_pointsat", "git tag --points-at HEAD", 0, 0, nil},
		{"v145_tag_merged", "git tag --merged main", 0, 0, nil},
		{"v146_tag_nomerged", "git tag --no-merged main", 0, 0, nil},
		{"v147_tag_sort", "git tag --sort=-creatordate", 0, 0, nil},
		{"v148_tag_format", "git tag --format=%(refname)", 0, 0, nil},
		{"v149_tag_list_eq", "git tag --list=x", 0, 0, nil},
		{"v150_tag_annotated", "git tag -a v1 -m msg", 0, 0, []string{"git_tag"}},
		{"v151_tag_force_after", "git tag v1.0 -f", 0, 0, []string{"git_tag"}},
		{"v152_tag_dashC", "git -C sub tag v1", 0, 0, []string{"git_tag"}},
		{"v153_tag_dashc", "git -c x=1 tag v2", 0, 0, []string{"git_tag"}},
		{"v154_tag_paginate", "git --paginate tag v1", 0, 0, []string{"git_tag"}},
		{"v155_tag_sudo", "sudo git tag v1", 0, 0, []string{"git_tag"}},
		{"v156_tag_and_push", "git tag v1 && git push origin v1", 0, 0, []string{"git_push", "git_tag"}},
		{"v157_tag_semi_push", "git tag; git push", 0, 0, []string{"git_push"}},
		{"v158_tag_create_then_list", "git tag v1; git tag -l", 0, 0, []string{"git_tag"}},
		{"v159_tag_ln", "git tag -ln", 0, 0, []string{"git_tag"}},
		{"v160_tag_nbsp_l", "git tag\u00a0-l", 0, 0, nil},
		{"v161_tag_paren", "git tag)", 0, 0, nil},
		{"v162_tag_after_done", "echo done; git tag v2", 0, 0, []string{"git_tag"}},
		{"v163_pub_npm", "npm publish", 0, 0, []string{"publish"}},
		{"v164_pub_npm_run", "npm run publish", 0, 0, []string{"publish"}},
		{"v165_pub_npm_unpublish", "npm unpublish", 0, 0, nil},
		{"v166_pub_npm_tag", "npm publish --tag beta", 0, 0, []string{"publish"}},
		{"v167_pub_npm_2sp", "npm  publish", 0, 0, []string{"publish"}},
		{"v168_pub_pnpm", "pnpm publish", 0, 0, []string{"publish"}},
		{"v169_pub_yarn", "yarn publish", 0, 0, []string{"publish"}},
		{"v170_pub_bun", "bun publish", 0, 0, []string{"publish"}},
		{"v171_pub_cargo", "cargo publish", 0, 0, []string{"publish"}},
		{"v172_pub_poetry", "poetry publish", 0, 0, []string{"publish"}},
		{"v173_pub_uv", "uv publish", 0, 0, []string{"publish"}},
		{"v174_pub_flit", "flit publish", 0, 0, []string{"publish"}},
		{"v175_pub_vsce", "vsce publish", 0, 0, []string{"publish"}},
		{"v176_pub_ovsx", "ovsx publish", 0, 0, []string{"publish"}},
		{"v177_pub_hatch", "hatch publish", 0, 0, []string{"publish"}},
		{"v178_pub_uvx", "uvx publish", 0, 0, nil},
		{"v179_pub_bunx", "bunx publish", 0, 0, nil},
		{"v180_pub_pnpm_reg", "pnpm --registry=x publish", 0, 0, nil},
		{"v181_pub_sudo", "sudo npm publish", 0, 0, []string{"publish"}},
		{"v182_pub_andand", "cd pkg && npm publish", 0, 0, []string{"publish"}},
		{"v183_pub_twine", "twine upload dist/*", 0, 0, []string{"publish"}},
		{"v184_pub_py3_twine", "python3 -m twine upload", 0, 0, []string{"publish"}},
		{"v185_pub_py_twine", "python -m twine upload", 0, 0, []string{"publish"}},
		{"v186_pub_py312_twine", "python3.12 -m twine upload", 0, 0, nil},
		{"v187_pub_gem", "gem push pkg-1.0.gem", 0, 0, []string{"publish"}},
		{"v188_pub_docker", "docker push image", 0, 0, []string{"publish"}},
		{"v189_pub_docker_image", "docker image push img", 0, 0, []string{"publish"}},
		{"v190_pub_dockerx", "docker pushx", 0, 0, nil},
		{"v191_pub_docker_2sp", "docker  image push", 0, 0, []string{"publish"}},
		{"v192_pub_gh_create", "gh release create v1", 0, 0, []string{"publish"}},
		{"v193_pub_gh_bare", "gh release create", 0, 0, []string{"publish"}},
		{"v194_pub_gh_createx", "gh release createx", 0, 0, nil},
		{"v195_pub_gh_sudo", "sudo gh release create v1 --notes x", 0, 0, []string{"publish"}},
		{"v196_pub_helm", "helm push chart", 0, 0, nil},
		{"v197_pub_npm_publishx", "npm publishx", 0, 0, nil},
		{"v198_len_100000_match", "git push --force ", 99983, 0, []string{"force_push", "git_push"}},
		{"v199_len_100001_miss", "git push --force ", 99984, 0, nil},
		{"v200_len_emoji_100000", "git push -f ", 0, 49994, []string{"force_push", "git_push"}},
		{"v201_len_emoji_100002", "git push -f ", 0, 49995, nil},
		{"v202_len_emoji_short", "git push -f ", 0, 10, []string{"force_push", "git_push"}},
		{"v203_text_howto", "how to git push --force safely", 0, 0, nil},
		{"v204_text_to_git", "to git push -f", 0, 0, nil},
		{"v205_ls", "ls -la", 0, 0, nil},
		{"v206_git_status", "git status", 0, 0, nil},
		{"v207_tag_list_status", "git tag -l && git status", 0, 0, nil},
		{"v208_paren_space_rm", "subshell ( rm -rf x", 0, 0, []string{"delete"}},
		{"v209_oror_rm", "true || rm -rf x", 0, 0, []string{"delete"}},
		{"v210_pipe_nospace", "echo a|rm x", 0, 0, []string{"delete"}},
	} {
		cmd := v.cmd + strings.Repeat("a", v.padA) + strings.Repeat("\U0001F680", v.padEmoji)
		var got []string
		for _, r := range Matching(cmd) {
			got = append(got, r.ID)
		}
		if strings.Join(got, ",") != strings.Join(v.want, ",") {
			t.Errorf("%s: Matching = %v, want %v (cmd %q)", v.id, got, v.want, utf16Preview(cmd))
		}
	}
}

// utf16Preview keeps a padded vector's failure line readable: its first runes and its length.
func utf16Preview(s string) string {
	const cut = 40
	r := []rune(s)
	if len(r) <= cut {
		return fmt.Sprintf("%s (%d utf16)", s, jsfmt.UTF16Len(s))
	}
	return fmt.Sprintf("%s… (%d utf16)", string(r[:cut]), jsfmt.UTF16Len(s))
}
